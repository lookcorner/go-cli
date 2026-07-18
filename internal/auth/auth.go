package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	defaultIssuer   = "https://auth.x.ai"
	defaultClientID = "b1a00492-073a-47ea-816f-4c329264a828"
	deviceGrantType = "urn:ietf:params:oauth:grant-type:device_code"
)

var defaultScopes = []string{
	"openid", "profile", "email", "offline_access", "grok-cli:access", "api:access",
	"conversations:read", "conversations:write",
}

type Config struct {
	Issuer   string
	ClientID string
	Scopes   []string
}

func DefaultConfig() Config {
	issuer, clientID := os.Getenv("GROK_OAUTH2_ISSUER"), os.Getenv("GROK_OAUTH2_CLIENT_ID")
	if strings.TrimSpace(issuer) == "" || strings.TrimSpace(clientID) == "" {
		issuer, clientID = os.Getenv("GROK_OIDC_ISSUER"), os.Getenv("GROK_OIDC_CLIENT_ID")
	}
	if strings.TrimSpace(issuer) == "" || strings.TrimSpace(clientID) == "" {
		issuer, clientID = defaultIssuer, defaultClientID
		if envEnabled(os.Getenv("GROK_LOCAL_AUTH")) {
			issuer = "http://localhost:22255"
		}
	}
	scopes := defaultScopes
	if value := firstEnv("GROK_OAUTH2_SCOPES", "GROK_OIDC_SCOPES"); value != "" {
		scopes = strings.Fields(value)
	}
	return Config{Issuer: strings.TrimRight(strings.TrimSpace(issuer), "/"), ClientID: strings.TrimSpace(clientID), Scopes: append([]string(nil), scopes...)}
}

func (c Config) Scope() string { return strings.TrimRight(c.Issuer, "/") + "::" + c.ClientID }

type Credential struct {
	Key          string     `json:"key"`
	AuthMode     string     `json:"auth_mode"`
	CreateTime   time.Time  `json:"create_time"`
	UserID       string     `json:"user_id"`
	Email        string     `json:"email,omitempty"`
	RefreshToken string     `json:"refresh_token,omitempty"`
	ExpiresAt    *time.Time `json:"expires_at,omitempty"`
	Issuer       string     `json:"oidc_issuer,omitempty"`
	ClientID     string     `json:"oidc_client_id,omitempty"`
}

type DeviceCode struct {
	VerificationURI         string
	VerificationURIComplete string
	UserCode                string
	code                    string
	interval                time.Duration
	expiresIn               time.Duration
}

type Client struct {
	HTTP  *http.Client
	Sleep func(context.Context, time.Duration) error
	Now   func() time.Time
}

func NewClient(httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	copy := *httpClient
	previousRedirect := copy.CheckRedirect
	copy.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if len(via) > 0 && (request.URL.Scheme != via[0].URL.Scheme || !strings.EqualFold(request.URL.Host, via[0].URL.Host)) {
			return http.ErrUseLastResponse
		}
		if previousRedirect != nil {
			return previousRedirect(request, via)
		}
		if len(via) >= 10 {
			return errors.New("stopped after 10 OAuth redirects")
		}
		return nil
	}
	return &Client{HTTP: &copy, Sleep: sleepContext, Now: time.Now}
}

func DefaultPath() (string, error) {
	if home := os.Getenv("GROK_HOME"); home != "" {
		return filepath.Join(home, "auth.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".grok", "auth.json"), nil
}

func (c *Client) RequestDeviceCode(ctx context.Context, cfg Config) (DeviceCode, error) {
	if err := validateVerificationURI(cfg.Issuer); err != nil {
		return DeviceCode{}, fmt.Errorf("invalid OAuth issuer: %w", err)
	}
	form := url.Values{"client_id": {cfg.ClientID}, "scope": {strings.Join(cfg.Scopes, " ")}, "referrer": {"grok-build"}}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(cfg.Issuer, "/")+"/oauth2/device/code", strings.NewReader(form.Encode()))
	if err != nil {
		return DeviceCode{}, err
	}
	setHeaders(request)
	response, err := c.HTTP.Do(request)
	if err != nil {
		return DeviceCode{}, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return DeviceCode{}, errors.New("device-code login is not enabled")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return DeviceCode{}, responseError("device code request", response)
	}
	var wire struct {
		DeviceCode              string `json:"device_code"`
		UserCode                string `json:"user_code"`
		VerificationURI         string `json:"verification_uri"`
		VerificationURIComplete string `json:"verification_uri_complete"`
		ExpiresIn               int64  `json:"expires_in"`
		Interval                int64  `json:"interval"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&wire); err != nil {
		return DeviceCode{}, fmt.Errorf("decode device code response: %w", err)
	}
	if wire.DeviceCode == "" || wire.UserCode == "" || !validUserCode(wire.UserCode) {
		return DeviceCode{}, errors.New("device code response is incomplete or invalid")
	}
	if err := validateVerificationURI(wire.VerificationURI); err != nil {
		return DeviceCode{}, err
	}
	if wire.VerificationURIComplete != "" {
		if err := validateVerificationURI(wire.VerificationURIComplete); err != nil {
			return DeviceCode{}, err
		}
	}
	interval := time.Duration(wire.Interval) * time.Second
	if interval < time.Second {
		interval = 5 * time.Second
	}
	expires := time.Duration(wire.ExpiresIn) * time.Second
	if expires < 10*time.Minute {
		expires = 10 * time.Minute
	}
	return DeviceCode{
		VerificationURI: wire.VerificationURI, VerificationURIComplete: wire.VerificationURIComplete,
		UserCode: wire.UserCode, code: wire.DeviceCode, interval: interval, expiresIn: expires,
	}, nil
}

func (c *Client) CompleteDeviceLogin(ctx context.Context, cfg Config, code DeviceCode) (Credential, error) {
	interval := code.interval
	deadline := c.Now().Add(code.expiresIn)
	for {
		if err := c.Sleep(ctx, interval); err != nil {
			return Credential{}, err
		}
		if c.Now().After(deadline) {
			return Credential{}, errors.New("device code expired")
		}
		form := url.Values{"grant_type": {deviceGrantType}, "device_code": {code.code}, "client_id": {cfg.ClientID}}
		credential, tokenError, err := c.exchange(ctx, cfg, form)
		if err != nil {
			return Credential{}, err
		}
		switch tokenError {
		case "":
			return credential, nil
		case "authorization_pending":
			continue
		case "slow_down":
			interval += 5 * time.Second
			continue
		case "access_denied":
			return Credential{}, errors.New("authorization denied")
		case "expired_token":
			return Credential{}, errors.New("device code expired")
		default:
			return Credential{}, fmt.Errorf("token exchange error: %s", tokenError)
		}
	}
}

func (c *Client) Refresh(ctx context.Context, cfg Config, credential Credential) (Credential, error) {
	if credential.RefreshToken == "" {
		return Credential{}, errors.New("OAuth credential has no refresh token")
	}
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {credential.RefreshToken}, "client_id": {cfg.ClientID}}
	refreshed, tokenError, err := c.exchange(ctx, cfg, form)
	if err != nil {
		return Credential{}, err
	}
	if tokenError != "" {
		return Credential{}, fmt.Errorf("refresh token exchange error: %s", tokenError)
	}
	if refreshed.RefreshToken == "" {
		refreshed.RefreshToken = credential.RefreshToken
	}
	if refreshed.UserID == "" {
		refreshed.UserID = credential.UserID
	}
	if refreshed.Email == "" {
		refreshed.Email = credential.Email
	}
	return refreshed, nil
}

func (c *Client) Resolve(ctx context.Context, path string, cfg Config) (string, error) {
	lock, err := acquireFileLock(ctx, path)
	if err != nil {
		return "", err
	}
	defer lock.release()
	credential, err := Load(path, cfg.Scope())
	if err != nil {
		return "", err
	}
	if credential.ExpiresAt == nil || credential.ExpiresAt.After(c.Now().Add(5*time.Minute)) {
		return credential.Key, nil
	}
	credential, err = c.Refresh(ctx, cfg, credential)
	if err != nil {
		return "", err
	}
	if err := saveCredential(path, cfg.Scope(), credential); err != nil {
		return "", err
	}
	return credential.Key, nil
}

func (c *Client) exchange(ctx context.Context, cfg Config, form url.Values) (Credential, string, error) {
	if err := validateVerificationURI(cfg.Issuer); err != nil {
		return Credential{}, "", fmt.Errorf("invalid OAuth issuer: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(cfg.Issuer, "/")+"/oauth2/token", strings.NewReader(form.Encode()))
	if err != nil {
		return Credential{}, "", err
	}
	setHeaders(request)
	response, err := c.HTTP.Do(request)
	if err != nil {
		return Credential{}, "", err
	}
	defer response.Body.Close()
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		var wire struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			ExpiresIn    int64  `json:"expires_in"`
			IDToken      string `json:"id_token"`
		}
		if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&wire); err != nil {
			return Credential{}, "", fmt.Errorf("decode token response: %w", err)
		}
		if wire.AccessToken == "" {
			return Credential{}, "", errors.New("token response has no access token")
		}
		now := c.Now().UTC()
		var expiresAt *time.Time
		if wire.ExpiresIn > 0 {
			value := now.Add(time.Duration(wire.ExpiresIn) * time.Second)
			expiresAt = &value
		}
		userID, email := jwtIdentity(wire.IDToken)
		return Credential{
			Key: wire.AccessToken, AuthMode: "oidc", CreateTime: now, UserID: userID, Email: email,
			RefreshToken: wire.RefreshToken, ExpiresAt: expiresAt, Issuer: cfg.Issuer, ClientID: cfg.ClientID,
		}, "", nil
	}
	var wire struct {
		Error string `json:"error"`
	}
	if json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&wire) != nil || wire.Error == "" {
		return Credential{}, "", fmt.Errorf("token endpoint returned %s", response.Status)
	}
	return Credential{}, wire.Error, nil
}

func setHeaders(request *http.Request) {
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("x-grok-client-version", "gork-go/0.1")
	request.Header.Set("x-grok-client-surface", "cli")
}

func responseError(action string, response *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	return fmt.Errorf("%s failed (%s): %s", action, response.Status, strings.TrimSpace(string(body)))
}

func validUserCode(code string) bool {
	for _, char := range code {
		if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '-') {
			return false
		}
	}
	return code != ""
}

func validateVerificationURI(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.User != nil {
		return errors.New("OAuth server returned an invalid verification URL")
	}
	if parsed.Scheme == "https" {
		return nil
	}
	host := parsed.Hostname()
	if parsed.Scheme == "http" && (host == "localhost" || host == "127.0.0.1" || host == "::1") {
		return nil
	}
	return errors.New("OAuth verification URL must use HTTPS")
}

func jwtIdentity(token string) (string, string) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return "", ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", ""
	}
	var claims struct {
		Subject string `json:"sub"`
		Email   string `json:"email"`
	}
	if json.Unmarshal(payload, &claims) != nil {
		return "", ""
	}
	return claims.Subject, claims.Email
}

func sleepContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func firstEnv(names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
	}
	return ""
}

func envEnabled(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "0", "false", "off", "no":
		return false
	default:
		return true
	}
}
