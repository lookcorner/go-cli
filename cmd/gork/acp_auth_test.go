package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/lookcorner/go-cli/internal/acp"
	"github.com/lookcorner/go-cli/internal/auth"
	"github.com/lookcorner/go-cli/internal/config"
)

func TestBuildACPAuthMethods(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	scope := auth.DefaultConfig().Scope()
	if err := auth.Save(path, scope, auth.Credential{Key: "oauth-token"}); err != nil {
		t.Fatal(err)
	}
	methods, defaultMethod := buildACPAuthMethods(config.Config{APIKey: "api-key"}, path, scope)
	if len(methods) != 3 || methods[0].ID != "xai.api_key" || methods[1].ID != "cached_token" || !methods[2].Interactive || defaultMethod != "cached_token" {
		t.Fatalf("methods=%#v default=%q", methods, defaultMethod)
	}
	methods, defaultMethod = buildACPAuthMethods(config.Config{APIKey: "oauth-token"}, filepath.Join(t.TempDir(), "missing.json"), scope)
	if len(methods) != 2 || methods[0].ID != "xai.api_key" || defaultMethod != "xai.api_key" {
		t.Fatalf("methods=%#v default=%q", methods, defaultMethod)
	}
	methods, defaultMethod = buildACPAuthMethods(config.Config{APIKey: "api-key", PreferredAuthMethod: "oidc"}, path, scope)
	if len(methods) != 2 || methods[0].ID != "cached_token" || methods[1].ID != "grok.com" || defaultMethod != "cached_token" {
		t.Fatalf("pinned methods=%#v default=%q", methods, defaultMethod)
	}
}

func TestACPLoginCoordinatorSwitchesToAPIKey(t *testing.T) {
	coordinator := newACPLoginCoordinator(nil, auth.DefaultConfig(), filepath.Join(t.TempDir(), "auth.json"))
	coordinator.appConfig = func() config.Config { return config.Config{} }
	coordinator.resolveAPIKey = func() (string, bool) { return "api-key", true }
	var sessionKey, methodID, stateToken, refreshToken string
	coordinator.setSessionKey = func(value string) { sessionKey = value }
	coordinator.setState = func(method, token string) { methodID, stateToken = method, token }
	coordinator.refreshModels = func(token string) { refreshToken = token }
	meta, err := coordinator.Authenticate(context.Background(), acp.AuthRequest{MethodID: "xai.api_key"})
	if err != nil || meta != nil || sessionKey != "api-key" || methodID != "xai.api_key" || stateToken != "api-key" || refreshToken != "api-key" {
		t.Fatalf("meta=%#v err=%v session=%q state=%q/%q refresh=%q", meta, err, sessionKey, methodID, stateToken, refreshToken)
	}
}

func TestACPLoginCoordinatorFallsBackFromCachedToken(t *testing.T) {
	coordinator := newACPLoginCoordinator(nil, auth.DefaultConfig(), filepath.Join(t.TempDir(), "auth.json"))
	cfg := config.Config{}
	coordinator.appConfig = func() config.Config { return cfg }
	coordinator.tokenProvider = func(context.Context, string) (string, error) { return "", errors.New("expired") }
	coordinator.resolveAPIKey = func() (string, bool) { return "api-key", true }
	var methodID string
	coordinator.setState = func(method, _ string) { methodID = method }
	if _, err := coordinator.Authenticate(context.Background(), acp.AuthRequest{MethodID: "cached_token"}); err != nil || methodID != "xai.api_key" {
		t.Fatalf("method=%q err=%v", methodID, err)
	}
	cfg.PreferredAuthMethod = "oidc"
	if _, err := coordinator.Authenticate(context.Background(), acp.AuthRequest{MethodID: "cached_token"}); err == nil {
		t.Fatal("pinned OIDC accepted an unavailable cached session")
	}
}

func TestACPAuthRuntimeProviderSelection(t *testing.T) {
	provider := func(context.Context, string) (string, error) { return "token", nil }
	runtime := &acpAuthRuntime{provider: provider}
	for _, method := range []string{"cached_token", "grok.com", "oidc"} {
		runtime.Set(method)
		if runtime.Provider() == nil {
			t.Fatalf("provider missing for %q", method)
		}
	}
	runtime.Set("xai.api_key")
	if runtime.Provider() != nil {
		t.Fatal("OAuth provider remained active for API-key auth")
	}
}
