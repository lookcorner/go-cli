package auth

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

type BrowserLogin struct {
	AuthorizationURL string
	listener         net.Listener
	oauth            oauth2.Config
	verifier         *oidc.IDTokenVerifier
	state            string
	nonce            string
	codeVerifier     string
	issuer           string
	httpClient       *http.Client
	now              func() time.Time
}

type loginCallback struct {
	code  string
	state string
	err   error
}

func (c *Client) StartBrowserLogin(ctx context.Context, cfg Config) (*BrowserLogin, error) {
	if err := validateVerificationURI(cfg.Issuer); err != nil {
		return nil, fmt.Errorf("invalid OIDC issuer: %w", err)
	}
	if cfg.ClientID == "" || len(cfg.Scopes) == 0 {
		return nil, errors.New("OIDC client ID and scopes are required")
	}
	provider, err := oidc.NewProvider(oidc.ClientContext(ctx, c.HTTP), cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("discover OIDC provider: %w", err)
	}
	endpoint := provider.Endpoint()
	if err := validateVerificationURI(endpoint.AuthURL); err != nil {
		return nil, fmt.Errorf("invalid OIDC authorization endpoint: %w", err)
	}
	if err := validateVerificationURI(endpoint.TokenURL); err != nil {
		return nil, fmt.Errorf("invalid OIDC token endpoint: %w", err)
	}
	port := 0
	if envEnabled(firstEnv("GROK_LOCAL_AUTH")) {
		port = 56121
	}
	listener, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return nil, fmt.Errorf("listen for OIDC callback: %w", err)
	}
	redirectURL := "http://" + listener.Addr().String() + "/callback"
	oauthConfig := oauth2.Config{
		ClientID: cfg.ClientID, Endpoint: endpoint, RedirectURL: redirectURL,
		Scopes: append([]string(nil), cfg.Scopes...),
	}
	state, err := randomURLToken(32)
	if err != nil {
		listener.Close()
		return nil, err
	}
	nonce, err := randomURLToken(32)
	if err != nil {
		listener.Close()
		return nil, err
	}
	codeVerifier := oauth2.GenerateVerifier()
	authURL := oauthConfig.AuthCodeURL(
		state, oauth2.AccessTypeOffline, oauth2.S256ChallengeOption(codeVerifier), oidc.Nonce(nonce),
	)
	parsed, err := url.Parse(authURL)
	if err != nil {
		listener.Close()
		return nil, err
	}
	query := parsed.Query()
	query.Set("referrer", "grok-build")
	if cfg.Audience != "" {
		query.Set("audience", cfg.Audience)
	}
	parsed.RawQuery = query.Encode()
	return &BrowserLogin{
		AuthorizationURL: parsed.String(), listener: listener, oauth: oauthConfig,
		verifier: provider.Verifier(&oidc.Config{
			ClientID: cfg.ClientID,
			SupportedSigningAlgs: []string{
				"RS256", "RS384", "RS512", "PS256", "PS384", "PS512", "ES256", "ES384", "EdDSA",
			},
		}), state: state,
		nonce: nonce, codeVerifier: codeVerifier, issuer: cfg.Issuer, httpClient: c.HTTP, now: c.Now,
	}, nil
}

func (l *BrowserLogin) Close() error {
	if l == nil || l.listener == nil {
		return nil
	}
	err := l.listener.Close()
	l.listener = nil
	return err
}

func (l *BrowserLogin) Complete(ctx context.Context, pastedInput io.Reader) (Credential, error) {
	if l == nil || l.listener == nil {
		return Credential{}, errors.New("OIDC browser login is not active")
	}
	callback := make(chan loginCallback, 1)
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: callbackHandler(callback)}
	serveDone := make(chan struct{})
	go func() {
		_ = server.Serve(l.listener)
		close(serveDone)
	}()
	defer func() {
		_ = server.Close()
		<-serveDone
		l.listener = nil
	}()
	if pastedInput != nil {
		go readPastedCallback(pastedInput, callback)
	}

	var result loginCallback
	select {
	case result = <-callback:
	case <-ctx.Done():
		return Credential{}, ctx.Err()
	}
	if result.err != nil {
		return Credential{}, result.err
	}
	if result.state != "" && subtle.ConstantTimeCompare([]byte(result.state), []byte(l.state)) != 1 {
		return Credential{}, errors.New("OIDC callback state mismatch")
	}
	exchangeCtx := oidc.ClientContext(ctx, l.httpClient)
	token, err := l.oauth.Exchange(exchangeCtx, result.code, oauth2.VerifierOption(l.codeVerifier))
	if err != nil {
		return Credential{}, fmt.Errorf("exchange OIDC authorization code: %w", err)
	}
	rawIDToken, _ := token.Extra("id_token").(string)
	if rawIDToken == "" {
		return Credential{}, errors.New("OIDC token response has no id_token")
	}
	idToken, err := l.verifier.Verify(exchangeCtx, rawIDToken)
	if err != nil {
		return Credential{}, fmt.Errorf("verify OIDC ID token: %w", err)
	}
	if subtle.ConstantTimeCompare([]byte(idToken.Nonce), []byte(l.nonce)) != 1 {
		return Credential{}, errors.New("OIDC ID token nonce mismatch")
	}
	var claims struct {
		Subject string `json:"sub"`
		Email   string `json:"email"`
	}
	if err := idToken.Claims(&claims); err != nil {
		return Credential{}, fmt.Errorf("decode OIDC ID token claims: %w", err)
	}
	if claims.Subject == "" {
		return Credential{}, errors.New("OIDC ID token has no subject")
	}
	created := l.now().UTC()
	var expiresAt *time.Time
	if !token.Expiry.IsZero() {
		value := token.Expiry.UTC()
		expiresAt = &value
	}
	return Credential{
		Key: token.AccessToken, AuthMode: "oidc", CreateTime: created,
		UserID: claims.Subject, Email: claims.Email, RefreshToken: token.RefreshToken,
		ExpiresAt: expiresAt, Issuer: l.issuer, ClientID: l.oauth.ClientID,
		TokenEndpoint: l.oauth.Endpoint.TokenURL,
	}, nil
}

func callbackHandler(callback chan<- loginCallback) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/callback" {
			http.NotFound(writer, request)
			return
		}
		result := parseCallback(request.URL.String())
		select {
		case callback <- result:
		default:
		}
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		if result.err != nil {
			fmt.Fprintf(writer, "<!doctype html><title>Access denied</title><p>%s</p>", html.EscapeString(result.err.Error()))
			return
		}
		io.WriteString(writer, "<!doctype html><title>Signed in</title><p>You can close this window and return to Gork.</p>")
	})
}

func readPastedCallback(input io.Reader, callback chan<- loginCallback) {
	scanner := bufio.NewScanner(input)
	for scanner.Scan() {
		if value := strings.TrimSpace(scanner.Text()); value != "" {
			select {
			case callback <- parseCallback(value):
			default:
			}
			return
		}
	}
}

func parseCallback(value string) loginCallback {
	value = strings.TrimSpace(value)
	if parsed, err := url.Parse(value); err == nil && (parsed.IsAbs() || parsed.RawQuery != "") {
		query := parsed.Query()
		if oauthError := query.Get("error"); oauthError != "" {
			description := query.Get("error_description")
			if description != "" {
				oauthError += ": " + description
			}
			return loginCallback{err: fmt.Errorf("OIDC authorization failed: %s", oauthError)}
		}
		if code := query.Get("code"); code != "" {
			return loginCallback{code: code, state: query.Get("state")}
		}
		return loginCallback{err: errors.New("OIDC callback URL has no code")}
	}
	if value == "" {
		return loginCallback{err: errors.New("OIDC authorization code is empty")}
	}
	return loginCallback{code: value}
}

func randomURLToken(size int) (string, error) {
	data := make([]byte, size)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}
