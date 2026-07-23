package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/lookcorner/go-cli/internal/acp"
	"github.com/lookcorner/go-cli/internal/api"
	"github.com/lookcorner/go-cli/internal/auth"
	"github.com/lookcorner/go-cli/internal/config"
)

type acpAuthRuntime struct {
	mu       sync.RWMutex
	methodID string
	provider api.TokenProvider
}

func (r *acpAuthRuntime) Set(methodID string) {
	r.mu.Lock()
	r.methodID = methodID
	r.mu.Unlock()
}

func (r *acpAuthRuntime) Method() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.methodID
}

func (r *acpAuthRuntime) Provider() api.TokenProvider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.methodID != "cached_token" && r.methodID != "grok.com" && r.methodID != "oidc" {
		return nil
	}
	return r.provider
}

func buildACPAuthMethods(cfg config.Config, authPath, scope string) ([]acp.AuthMethod, string) {
	hasAPIKey := resolveACPAPIKey(cfg, authPath) != "" || hasModelAPIKey(cfg)
	_, cachedErr := auth.Load(authPath, scope)
	hasCached := cachedErr == nil
	if cfg.DisableAPIKeyAuth || cfg.ForceLoginTeamConfigured || cfg.PreferredAuthMethod == "oidc" {
		hasAPIKey = false
	}
	if cfg.PreferredAuthMethod == "api_key" {
		if !hasAPIKey {
			return nil, ""
		}
		return []acp.AuthMethod{apiKeyACPAuthMethod()}, "xai.api_key"
	}

	methods := make([]acp.AuthMethod, 0, 3)
	defaultMethod := ""
	if hasAPIKey {
		methods = append(methods, apiKeyACPAuthMethod())
		defaultMethod = "xai.api_key"
	}
	if hasCached {
		methods = append(methods, acp.AuthMethod{ID: "cached_token", Name: "cached_token", Description: "Cached token from ~/.grok/auth.json"})
		defaultMethod = "cached_token"
	}
	methodID := "grok.com"
	if strings.TrimSpace(os.Getenv("GROK_OIDC_ISSUER")) != "" {
		methodID = "oidc"
	}
	interactive := acp.AuthMethod{ID: methodID, Name: "Grok", Description: "Sign in with Grok", Interactive: true}
	if cfg.AuthProviderCommand != "" {
		interactive.Meta = map[string]any{"external_provider": true}
	}
	methods = append(methods, interactive)
	return methods, defaultMethod
}

func apiKeyACPAuthMethod() acp.AuthMethod {
	return acp.AuthMethod{ID: "xai.api_key", Name: "xai.api_key", Description: "XAI_API_KEY or api_key/env_key in config.toml"}
}

func hasModelAPIKey(cfg config.Config) bool {
	for _, profile := range cfg.ModelProfiles {
		if strings.TrimSpace(profile.APIKey) != "" {
			return true
		}
	}
	return false
}

func resolveACPAPIKey(cfg config.Config, authPath string) string {
	if strings.TrimSpace(cfg.APIKey) != "" {
		return cfg.APIKey
	}
	if key, ok := auth.ReadAPIKeyEnvironment(); ok {
		return key
	}
	credential, err := auth.Load(authPath, auth.APIKeyScope)
	if err == nil {
		return credential.Key
	}
	return ""
}

type acpLoginCoordinator struct {
	mu            sync.Mutex
	client        *auth.Client
	authConfig    auth.Config
	authPath      string
	appConfig     func() config.Config
	applySettings func(*config.RemoteSettings)
	setSessionKey func(string)
	setState      func(string, string)
	refreshModels func(string)
	resolveAPIKey func() (string, bool)
	tokenProvider api.TokenProvider
	stderr        io.Writer
	active        *auth.BrowserLogin
	inFlight      bool
	urlReady      chan struct{}
	urlPublished  bool
	urlConsumed   bool
	url           acp.AuthURLResult
}

func newACPLoginCoordinator(client *auth.Client, authConfig auth.Config, authPath string) *acpLoginCoordinator {
	return &acpLoginCoordinator{client: client, authConfig: authConfig, authPath: authPath, urlReady: make(chan struct{})}
}

func (c *acpLoginCoordinator) Authenticate(ctx context.Context, request acp.AuthRequest) (*acp.AuthMeta, error) {
	switch request.MethodID {
	case "xai.api_key":
		cfg := c.appConfig()
		if cfg.DisableAPIKeyAuth || cfg.ForceLoginTeamConfigured || cfg.PreferredAuthMethod == "oidc" {
			return nil, errors.New("API-key auth is disabled by your administrator")
		}
		key, available := "", false
		if c.resolveAPIKey != nil {
			key, available = c.resolveAPIKey()
		} else {
			key = resolveACPAPIKey(cfg, c.authPath)
			available = key != "" || hasModelAPIKey(cfg)
		}
		if !available {
			return nil, errors.New("set XAI_API_KEY or add api_key/env_key to config.toml")
		}
		if c.setSessionKey != nil {
			c.setSessionKey(key)
		}
		if c.setState != nil {
			c.setState("xai.api_key", key)
		}
		if c.refreshModels != nil {
			c.refreshModels(key)
		}
		return nil, nil
	case "cached_token":
		if forceInteractive, _ := request.Meta["force_interactive"].(bool); forceInteractive {
			request.MethodID = "oidc"
			return c.authenticateInteractive(ctx, request)
		}
		if c.tokenProvider == nil {
			return c.authenticateAfterCachedUnavailable(ctx, request, errors.New("no cached session"))
		}
		token, err := c.tokenProvider(ctx, "")
		if err != nil {
			return c.authenticateAfterCachedUnavailable(ctx, request, err)
		}
		credential, err := auth.Load(c.authPath, c.authConfig.Scope())
		if err != nil {
			return c.authenticateAfterCachedUnavailable(ctx, request, err)
		}
		credential.Key = token
		return c.finish(ctx, request.MethodID, credential)
	case "grok.com", "oidc":
		return c.authenticateInteractive(ctx, request)
	default:
		return nil, fmt.Errorf("unknown authentication method %q", request.MethodID)
	}
}

func (c *acpLoginCoordinator) authenticateAfterCachedUnavailable(ctx context.Context, request acp.AuthRequest, cause error) (*acp.AuthMeta, error) {
	cfg := c.appConfig()
	if cfg.PreferredAuthMethod != "" {
		return nil, fmt.Errorf("cached session is unavailable: %w", cause)
	}
	key, available := "", false
	if c.resolveAPIKey != nil {
		key, available = c.resolveAPIKey()
	} else {
		key = resolveACPAPIKey(cfg, c.authPath)
		available = key != "" || hasModelAPIKey(cfg)
	}
	if available {
		request.MethodID = "xai.api_key"
		return c.Authenticate(ctx, request)
	}
	request.MethodID = "grok.com"
	return c.authenticateInteractive(ctx, request)
}

func (c *acpLoginCoordinator) authenticateInteractive(ctx context.Context, request acp.AuthRequest) (*acp.AuthMeta, error) {
	if !c.beginInteractive() {
		return nil, errors.New("an authentication session is already active")
	}
	defer c.endInteractive()
	if reauth, _ := request.Meta["reauth"].(bool); reauth {
		_ = auth.Remove(c.authPath, c.authConfig.Scope())
	}
	if c.appConfig().AuthProviderCommand != "" {
		mode := "command"
		c.publishURL(acp.AuthURLResult{ExternalProvider: true, Mode: &mode})
		token, err := c.tokenProvider(ctx, "")
		if err != nil {
			return nil, err
		}
		credential, err := auth.Load(c.authPath, c.authConfig.Scope())
		if err != nil {
			return nil, err
		}
		credential.Key = token
		return c.finish(ctx, request.MethodID, credential)
	}
	if headless, _ := request.Meta["headless"].(bool); headless {
		code, err := c.client.RequestDeviceCode(ctx, c.authConfig)
		if err != nil {
			c.publishURL(acp.AuthURLResult{})
			return nil, err
		}
		url := code.VerificationURIComplete
		if url == "" {
			url = code.VerificationURI
		}
		mode := "device"
		c.publishURL(acp.AuthURLResult{AuthURL: &url, Mode: &mode})
		credential, err := c.client.CompleteDeviceLogin(ctx, c.authConfig, code)
		if err != nil {
			return nil, err
		}
		return c.finish(ctx, request.MethodID, credential)
	}

	login, err := c.client.StartBrowserLogin(ctx, c.authConfig)
	if err != nil {
		c.publishURL(acp.AuthURLResult{})
		return nil, err
	}
	c.mu.Lock()
	c.active = login
	c.mu.Unlock()
	defer login.Close()
	url, mode := login.AuthorizationURL, "loopback"
	c.publishURL(acp.AuthURLResult{AuthURL: &url, Mode: &mode})
	loginCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	credential, err := login.Complete(loginCtx, nil)
	if err != nil {
		return nil, err
	}
	return c.finish(ctx, request.MethodID, credential)
}

func (c *acpLoginCoordinator) finish(ctx context.Context, methodID string, credential auth.Credential) (*acp.AuthMeta, error) {
	cfg := c.appConfig()
	credential = c.client.Enrich(ctx, cfg.ProxyBaseURL, "", credential)
	if err := auth.Save(c.authPath, c.authConfig.Scope(), credential); err != nil {
		return nil, fmt.Errorf("save OAuth credentials: %w", err)
	}
	settingsCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	remote := config.FetchRemoteSettingsForSession(settingsCtx, cfg.ProxyBaseURL, credential.Key, credential.UserID, credential.Email, nil)
	cancel()
	if remote != nil {
		c.applySettings(remote)
		cfg = c.appConfig()
	}
	if c.setSessionKey != nil {
		c.setSessionKey(credential.Key)
	}
	if c.setState != nil {
		c.setState(methodID, credential.Key)
	}
	if c.refreshModels != nil {
		c.refreshModels(credential.Key)
	}
	if _, _, err := syncManagedPolicy(ctx, cfg, &credential, 2); err != nil && c.stderr != nil {
		fmt.Fprintf(c.stderr, "Managed configuration was not updated: %v\n", err)
	}
	allowed := credential.AuthMode == "external" || cfg.AllowAccess != nil && *cfg.AllowAccess
	return acpAuthMeta(cfg, credential, allowed), nil
}

func (c *acpLoginCoordinator) beginInteractive() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.inFlight {
		return false
	}
	c.inFlight = true
	c.active = nil
	c.urlReady = make(chan struct{})
	c.urlPublished = false
	c.urlConsumed = false
	c.url = acp.AuthURLResult{}
	return true
}

func (c *acpLoginCoordinator) endInteractive() {
	c.mu.Lock()
	c.inFlight = false
	c.active = nil
	if !c.urlPublished {
		c.urlPublished = true
		close(c.urlReady)
	}
	c.mu.Unlock()
}

func (c *acpLoginCoordinator) publishURL(result acp.AuthURLResult) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.urlPublished {
		return
	}
	c.url, c.urlPublished = result, true
	close(c.urlReady)
}

func (c *acpLoginCoordinator) GetURL(ctx context.Context) (acp.AuthURLResult, error) {
	for {
		c.mu.Lock()
		if c.urlPublished {
			if c.urlConsumed {
				c.mu.Unlock()
				return acp.AuthURLResult{}, nil
			}
			c.urlConsumed = true
			result := c.url
			c.mu.Unlock()
			return result, nil
		}
		ready := c.urlReady
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return acp.AuthURLResult{}, ctx.Err()
		case <-ready:
		}
	}
}

func (c *acpLoginCoordinator) SubmitCode(code string) error {
	c.mu.Lock()
	login := c.active
	c.mu.Unlock()
	if login == nil {
		return errors.New("no pending auth session")
	}
	return login.Submit(code)
}
