package mcp

import (
	"context"
	"errors"
	"strings"
)

var errMCPAuthRefreshUnavailable = errors.New("MCP OAuth refresh is unavailable")

type httpAuthState struct {
	path         string
	serverURL    string
	tokenURL     string
	clientSecret string
	staticAuth   bool
}

func prepareHTTPHeaders(cfg HTTPConfig) map[string]string {
	headers := cloneHeaders(cfg.Headers)
	path := ""
	if cfg.Auth != nil {
		path = cfg.Auth.CredentialsPath
	}
	return ApplyCredentialHeaders(headers, cfg.Name, cfg.URL, path)
}

func newHTTPAuthState(cfg HTTPConfig) *httpAuthState {
	if cfg.Auth == nil && !hasAuthorizationHeader(cfg.Headers) {
		// Still allow attach from the default store; refresh needs TokenURL.
		path, err := DefaultCredentialsPath()
		if err != nil {
			return nil
		}
		return &httpAuthState{path: path, serverURL: cfg.URL}
	}
	if cfg.Auth == nil {
		return &httpAuthState{staticAuth: hasAuthorizationHeader(cfg.Headers), serverURL: cfg.URL}
	}
	path := strings.TrimSpace(cfg.Auth.CredentialsPath)
	if path == "" {
		var err error
		path, err = DefaultCredentialsPath()
		if err != nil {
			path = ""
		}
	}
	return &httpAuthState{
		path:         path,
		serverURL:    cfg.URL,
		tokenURL:     strings.TrimSpace(cfg.Auth.TokenURL),
		clientSecret: cfg.Auth.ClientSecret,
		staticAuth:   hasAuthorizationHeader(cfg.Headers),
	}
}

func (c *Client) refreshAuthorization(ctx context.Context) error {
	if c == nil || c.auth == nil || c.auth.staticAuth || c.auth.path == "" {
		return errMCPAuthRefreshUnavailable
	}
	tokenURL := c.auth.tokenURL
	if tokenURL == "" {
		meta, err := DiscoverOAuthMetadata(ctx, c.auth.serverURL, c.httpClient)
		if err != nil {
			return err
		}
		tokenURL = strings.TrimSpace(meta.TokenEndpoint)
		c.auth.tokenURL = tokenURL
	}
	if tokenURL == "" {
		return errMCPAuthRefreshUnavailable
	}
	updated, err := RefreshStoredCredentials(ctx, c.auth.path, c.name, c.auth.serverURL, tokenURL, c.auth.clientSecret, c.httpClient)
	if err != nil {
		return err
	}
	token := updated.AccessToken()
	if token == "" {
		return errMCPAuthRefreshUnavailable
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.headers == nil {
		c.headers = make(map[string]string)
	}
	for key := range c.headers {
		if strings.EqualFold(key, "Authorization") {
			delete(c.headers, key)
		}
	}
	c.headers["Authorization"] = "Bearer " + token
	return nil
}
