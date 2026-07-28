package mcp

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
)

const mcpOAuthClientName = "Grok"

// AuthenticateOpts configures interactive MCP OAuth enrollment.
type AuthenticateOpts struct {
	CredentialsPath string
	ClientID        string
	ClientSecret    string
	Scopes          []string
	CallbackPort    uint16
	OpenURL         func(string) bool
	HTTPClient      *http.Client
	Force           bool
	// PastedInput, when set, reads a callback URL / "code state" line for
	// headless clients that cannot reach the loopback redirect.
	PastedInput io.Reader
	// Metadata, when set, skips discovery (tests / pre-discovered AS).
	Metadata *OAuthMetadata
}

// AuthenticateResult is the outcome of a successful enrollment.
type AuthenticateResult struct {
	Credentials StoredCredentials
	TokenURL    string
	Metadata    OAuthMetadata
}

type oauthCallback struct {
	code  string
	state string
	iss   string
	err   error
}

// authenticateMCPServerFlow runs browser PKCE enrollment (with DCR when needed)
// and persists tokens to mcp_credentials.json. Callers should prefer
// AuthenticateMCPServer for in-process/cross-process dedup.
func authenticateMCPServerFlow(ctx context.Context, name, serverURL string, opts AuthenticateOpts) (AuthenticateResult, error) {
	name = strings.TrimSpace(name)
	serverURL = strings.TrimSpace(serverURL)
	if name == "" || serverURL == "" {
		return AuthenticateResult{}, errors.New("MCP server name and URL are required")
	}
	client := opts.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	path := strings.TrimSpace(opts.CredentialsPath)
	if path == "" {
		var err error
		path, err = DefaultCredentialsPath()
		if err != nil {
			return AuthenticateResult{}, err
		}
	}

	if !opts.Force {
		if store, err := LoadCredentialStore(path); err == nil {
			if creds, ok := store.Get(name, serverURL); ok && creds.AccessToken() != "" {
				meta := OAuthMetadata{}
				if opts.Metadata != nil {
					meta = *opts.Metadata
				}
				return AuthenticateResult{Credentials: creds, TokenURL: meta.TokenEndpoint, Metadata: meta}, nil
			}
		}
	}

	var meta OAuthMetadata
	if opts.Metadata != nil {
		meta = *opts.Metadata
	} else {
		var err error
		meta, err = DiscoverOAuthMetadata(ctx, serverURL, client)
		if err != nil {
			return AuthenticateResult{}, err
		}
	}
	if strings.TrimSpace(meta.AuthorizationEndpoint) == "" || strings.TrimSpace(meta.TokenEndpoint) == "" {
		return AuthenticateResult{}, errors.New("OAuth metadata is incomplete")
	}

	scopes := append([]string(nil), opts.Scopes...)
	if len(scopes) == 0 {
		scopes = append([]string(nil), meta.ScopesSupported...)
	}

	port := int(opts.CallbackPort)
	listener, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return AuthenticateResult{}, fmt.Errorf("listen for MCP OAuth callback: %w", err)
	}
	redirectURL := "http://" + listener.Addr().String() + "/callback"

	clientID := strings.TrimSpace(opts.ClientID)
	clientSecret := opts.ClientSecret
	if clientID == "" {
		if strings.TrimSpace(meta.RegistrationEndpoint) == "" {
			listener.Close()
			return AuthenticateResult{}, errors.New("MCP OAuth requires oauth_client_id or a registration_endpoint")
		}
		registered, err := registerOAuthClient(ctx, client, meta.RegistrationEndpoint, redirectURL, scopes)
		if err != nil {
			listener.Close()
			return AuthenticateResult{}, err
		}
		clientID, clientSecret = registered.ClientID, registered.ClientSecret
	}

	state, err := randomOAuthToken(32)
	if err != nil {
		listener.Close()
		return AuthenticateResult{}, err
	}
	codeVerifier := oauth2.GenerateVerifier()
	challenge := s256Challenge(codeVerifier)

	authURL, err := buildAuthorizationURL(meta.AuthorizationEndpoint, clientID, redirectURL, state, challenge, scopes)
	if err != nil {
		listener.Close()
		return AuthenticateResult{}, err
	}

	callback := make(chan oauthCallback, 1)
	unregister := registerMCPAuthSubmit(name, callback)
	defer unregister()
	if opts.PastedInput != nil {
		go readPastedMCPCallback(opts.PastedInput, callback)
	}
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: mcpOAuthCallbackHandler(callback)}
	serveDone := make(chan struct{})
	go func() {
		_ = server.Serve(listener)
		close(serveDone)
	}()
	defer func() {
		_ = server.Close()
		<-serveDone
	}()

	opened := false
	if opts.OpenURL != nil {
		opened = opts.OpenURL(authURL)
	}
	_ = opened

	tokenBefore := storedAccessToken(path, name, serverURL)
	poll := time.NewTicker(2 * time.Second)
	defer poll.Stop()

	var result oauthCallback
waitCallback:
	for {
		select {
		case result = <-callback:
			break waitCallback
		case <-poll.C:
			if token := storedAccessToken(path, name, serverURL); token != "" && token != tokenBefore {
				if store, err := LoadCredentialStore(path); err == nil {
					if creds, ok := store.Get(name, serverURL); ok && creds.AccessToken() != "" {
						return AuthenticateResult{Credentials: creds, TokenURL: meta.TokenEndpoint, Metadata: meta}, nil
					}
				}
			}
		case <-ctx.Done():
			return AuthenticateResult{}, ctx.Err()
		}
	}
	if result.err != nil {
		return AuthenticateResult{}, result.err
	}
	if subtle.ConstantTimeCompare([]byte(result.state), []byte(state)) != 1 {
		return AuthenticateResult{}, errors.New("MCP OAuth callback state mismatch")
	}

	token, err := exchangeAuthorizationCode(ctx, client, meta.TokenEndpoint, clientID, clientSecret, redirectURL, result.code, codeVerifier)
	if err != nil {
		return AuthenticateResult{}, err
	}
	now := time.Now().Unix()
	creds := StoredCredentials{
		ClientID: clientID,
		TokenResponse: &TokenResponse{
			AccessToken:  token.AccessToken,
			TokenType:    firstNonEmpty(token.TokenType, "bearer"),
			ExpiresIn:    token.ExpiresIn,
			RefreshToken: token.RefreshToken,
			Scope:        token.Scope,
		},
		GrantedScopes:   scopes,
		TokenReceivedAt: &now,
	}
	if err := InsertAndSave(ctx, path, name, serverURL, creds); err != nil {
		return AuthenticateResult{}, err
	}
	return AuthenticateResult{Credentials: creds, TokenURL: meta.TokenEndpoint, Metadata: meta}, nil
}

type dcrResponse struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
}

func registerOAuthClient(ctx context.Context, client *http.Client, registrationEndpoint, redirectURL string, scopes []string) (dcrResponse, error) {
	payload := map[string]any{
		"client_name":                mcpOAuthClientName,
		"redirect_uris":              []string{redirectURL},
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
	}
	if len(scopes) > 0 {
		payload["scope"] = strings.Join(scopes, " ")
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return dcrResponse{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, registrationEndpoint, strings.NewReader(string(body)))
	if err != nil {
		return dcrResponse{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return dcrResponse{}, fmt.Errorf("MCP OAuth DCR: %w", err)
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return dcrResponse{}, fmt.Errorf("MCP OAuth DCR returned %s: %s", response.Status, strings.TrimSpace(string(data)))
	}
	var registered dcrResponse
	if err := json.Unmarshal(data, &registered); err != nil {
		return dcrResponse{}, fmt.Errorf("decode MCP OAuth DCR: %w", err)
	}
	if strings.TrimSpace(registered.ClientID) == "" {
		return dcrResponse{}, errors.New("MCP OAuth DCR omitted client_id")
	}
	return registered, nil
}

func buildAuthorizationURL(endpoint, clientID, redirectURL, state, challenge string, scopes []string) (string, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	query := parsed.Query()
	query.Set("response_type", "code")
	query.Set("client_id", clientID)
	query.Set("redirect_uri", redirectURL)
	query.Set("state", state)
	query.Set("code_challenge", challenge)
	query.Set("code_challenge_method", "S256")
	if len(scopes) > 0 {
		query.Set("scope", strings.Join(scopes, " "))
	}
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func exchangeAuthorizationCode(ctx context.Context, client *http.Client, tokenURL, clientID, clientSecret, redirectURL, code, verifier string) (TokenResponse, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", redirectURL)
	form.Set("client_id", clientID)
	form.Set("code_verifier", verifier)
	if strings.TrimSpace(clientSecret) != "" {
		form.Set("client_secret", clientSecret)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return TokenResponse{}, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return TokenResponse{}, fmt.Errorf("MCP OAuth token exchange: %w", err)
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return TokenResponse{}, fmt.Errorf("MCP OAuth token exchange returned %s: %s", response.Status, strings.TrimSpace(string(data)))
	}
	var token TokenResponse
	if err := json.Unmarshal(data, &token); err != nil {
		return TokenResponse{}, fmt.Errorf("decode MCP OAuth token: %w", err)
	}
	if strings.TrimSpace(token.AccessToken) == "" {
		return TokenResponse{}, errors.New("MCP OAuth token exchange omitted access_token")
	}
	return token, nil
}

func mcpOAuthCallbackHandler(callback chan<- oauthCallback) http.Handler {
	var once sync.Once
	deliver := func(result oauthCallback) {
		once.Do(func() {
			select {
			case callback <- result:
			default:
			}
		})
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/callback" {
			http.NotFound(w, r)
			return
		}
		query := r.URL.Query()
		if errMsg := query.Get("error"); errMsg != "" {
			desc := query.Get("error_description")
			if desc == "" {
				desc = errMsg
			}
			deliver(oauthCallback{err: fmt.Errorf("MCP OAuth denied: %s", desc)})
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, "<html><body><p>Authentication failed. You can close this window.</p></body></html>")
			return
		}
		code := query.Get("code")
		state := query.Get("state")
		if code == "" || state == "" {
			deliver(oauthCallback{err: errors.New("MCP OAuth callback missing code or state")})
			http.Error(w, "missing code or state", http.StatusBadRequest)
			return
		}
		deliver(oauthCallback{code: code, state: state, iss: query.Get("iss")})
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, "<html><body><p>Authentication complete for "+html.EscapeString(mcpOAuthClientName)+". You can close this window.</p></body></html>")
	})
}

func s256Challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func randomOAuthToken(size int) (string, error) {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// NeedsMCPAuth reports whether an HTTP/SSE server likely needs interactive OAuth.
func NeedsMCPAuth(server ServerConfig, credentialsPath string) bool {
	if strings.TrimSpace(server.URL) == "" {
		return false
	}
	if hasAuthorizationHeader(server.Headers) {
		return false
	}
	path := strings.TrimSpace(credentialsPath)
	if path == "" {
		var err error
		path, err = DefaultCredentialsPath()
		if err != nil {
			return true
		}
	}
	store, err := LoadCredentialStore(path)
	if err != nil {
		return true
	}
	creds, ok := store.Get(server.Name, server.URL)
	return !ok || creds.AccessToken() == ""
}
