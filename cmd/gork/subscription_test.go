package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lookcorner/go-cli/internal/auth"
	"github.com/lookcorner/go-cli/internal/config"
)

func TestACPSubscriptionCheckerRefreshesSettingsAndModels(t *testing.T) {
	path, scope := filepath.Join(t.TempDir(), "auth.json"), "issuer::client"
	if err := auth.Save(path, scope, auth.Credential{Key: "old-token", AuthMode: "oidc", UserID: "user-1", Email: "user@example.com"}); err != nil {
		t.Fatal(err)
	}
	requests := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests[request.URL.Path]++
		switch request.URL.Path {
		case "/v1/user":
			fmt.Fprint(writer, `{"userId":"user-1","subscriptionTier":"SuperGrokPro"}`)
		case "/v1/settings":
			if request.Header.Get("x-userid") != "user-1" || request.Header.Get("x-email") != "user@example.com" {
				t.Errorf("identity headers=%#v", request.Header)
			}
			fmt.Fprint(writer, `{"allow_access":true,"subscription_tier_display":"SuperGrok Heavy","on_demand_enabled":true}`)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	cfg := config.Config{ProxyBaseURL: server.URL + "/v1"}
	refreshToken := testSubscriptionJWT(5)
	providerCalls := 0
	var refreshedTier, refreshedToken string
	checker := &acpSubscriptionChecker{
		authPath: path, scope: scope, http: server.Client(), config: func() config.Config { return cfg },
		applySettings: func(remote *config.RemoteSettings) { cfg.ApplyRemoteSettings(remote) },
		tokenProvider: func(_ context.Context, rejected string) (string, error) {
			providerCalls++
			if providerCalls == 1 && rejected != "old-token" {
				t.Fatalf("first rejected token=%q", rejected)
			}
			return refreshToken, nil
		},
		refreshModels: func(tier, token string) { refreshedTier, refreshedToken = tier, token },
	}
	result := checker.Check(context.Background())
	if !result.Authenticated || result.Meta == nil || result.Meta.Email == nil || *result.Meta.Email != "user@example.com" || result.Meta.Gate != nil || result.Meta.SubscriptionTier == nil || *result.Meta.SubscriptionTier != "SuperGrok Heavy" {
		t.Fatalf("result=%#v", result)
	}
	if providerCalls != 2 || refreshedTier != "SuperGrokPro" || refreshedToken != refreshToken || requests["/v1/user"] != 1 || requests["/v1/settings"] != 1 || cfg.AllowAccess == nil || !*cfg.AllowAccess {
		t.Fatalf("providerCalls=%d refresh=%q/%q requests=%#v cfg=%#v", providerCalls, refreshedTier, refreshedToken, requests, cfg)
	}
}

func TestACPSubscriptionCheckerKeepsGateWithoutExplicitAccess(t *testing.T) {
	for _, test := range []struct {
		name        string
		user        string
		settings    string
		wantRefresh int
		wantMessage string
	}{
		{name: "settings deny", user: `{"userId":"user-1","subscriptionTier":"GrokPro"}`, settings: `{"allow_access":false,"gate_message":"Not enabled","gate_url":"https://example.com","gate_label":"Learn more"}`, wantRefresh: 1, wantMessage: "Not enabled"},
		{name: "missing access", user: `{"userId":"user-1","subscriptionTier":"GrokPro"}`, settings: `{}`, wantRefresh: 1, wantMessage: "A subscription is required."},
		{name: "free", user: `{"userId":"user-1","subscriptionTier":"Free"}`, wantMessage: "A subscription is required."},
		{name: "check failure", user: `unavailable`, wantMessage: "A subscription is required."},
	} {
		t.Run(test.name, func(t *testing.T) {
			path, scope := filepath.Join(t.TempDir(), "auth.json"), "issuer::client"
			if err := auth.Save(path, scope, auth.Credential{Key: "token", AuthMode: "oidc"}); err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.URL.Path == "/v1/settings" {
					fmt.Fprint(writer, test.settings)
					return
				}
				if test.name == "check failure" {
					writer.WriteHeader(http.StatusServiceUnavailable)
				}
				fmt.Fprint(writer, test.user)
			}))
			defer server.Close()
			cfg := config.Config{ProxyBaseURL: server.URL + "/v1"}
			refreshes, modelRefreshes := 0, 0
			checker := &acpSubscriptionChecker{
				authPath: path, scope: scope, http: server.Client(), config: func() config.Config { return cfg },
				applySettings: func(remote *config.RemoteSettings) { cfg.ApplyRemoteSettings(remote) },
				tokenProvider: func(context.Context, string) (string, error) { refreshes++; return "token", nil },
				refreshModels: func(string, string) { modelRefreshes++ },
			}
			result := checker.Check(context.Background())
			if !result.Authenticated || result.Meta == nil || result.Meta.Gate == nil || result.Meta.Gate.Message != test.wantMessage || refreshes != test.wantRefresh || modelRefreshes != 0 {
				t.Fatalf("result=%#v refreshes=%d modelRefreshes=%d", result, refreshes, modelRefreshes)
			}
		})
	}
}

func TestACPSubscriptionCheckerBypassesOAuthGateForStaticCredentials(t *testing.T) {
	for _, cfg := range []config.Config{{APIKey: "api-key"}, {DeploymentKey: "deployment-key"}, {}} {
		checker := &acpSubscriptionChecker{config: func() config.Config { return cfg }}
		result := checker.Check(context.Background())
		if cfg.APIKey == "" && cfg.DeploymentKey == "" {
			if result.Authenticated || result.Meta != nil {
				t.Fatalf("empty credentials result=%#v", result)
			}
			continue
		}
		if !result.Authenticated || result.Meta == nil || result.Meta.Gate != nil {
			t.Fatalf("static credentials result=%#v", result)
		}
		if cfg.APIKey != "" && (result.Meta.SubscriptionTier == nil || *result.Meta.SubscriptionTier != "api_key") {
			t.Fatalf("API key tier=%#v", result.Meta.SubscriptionTier)
		}
	}
	path, scope := filepath.Join(t.TempDir(), "auth.json"), "issuer::client"
	if err := auth.Save(path, scope, auth.Credential{Key: "external-token", AuthMode: "external"}); err != nil {
		t.Fatal(err)
	}
	checker := &acpSubscriptionChecker{
		authPath: path, scope: scope, tokenProvider: func(context.Context, string) (string, error) { return "external-token", nil },
		config: func() config.Config { return config.Config{} },
	}
	result := checker.Check(context.Background())
	if !result.Authenticated || result.Meta == nil || result.Meta.Gate != nil || result.Meta.AuthMode == nil || *result.Meta.AuthMode != "External" {
		t.Fatalf("external credentials result=%#v", result)
	}
}

func TestACPSubscriptionCatalogRefresherMatchesAndRetriesJWT(t *testing.T) {
	for _, test := range []struct {
		name         string
		initialTier  int
		providerTier int
		wantCalls    int32
	}{
		{name: "matching", initialTier: 5},
		{name: "stale retry", initialTier: 0, providerTier: 5, wantCalls: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("GROK_HOME", home)
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.URL.Path != "/v1/models" {
					t.Errorf("path=%q", request.URL.Path)
				}
				fmt.Fprint(writer, `{"data":[{"id":"paid-model","model":"paid-model","context_window":1000}]}`)
			}))
			defer server.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var calls atomic.Int32
			reloaded := make(chan struct{}, 1)
			cfg := config.Config{BaseURL: server.URL + "/v1", ProxyBaseURL: server.URL + "/v1", HTTPTimeout: time.Second}
			refresher := &acpSubscriptionCatalogRefresher{
				ctx: ctx, config: func() config.Config { return cfg }, authPath: filepath.Join(home, "auth.json"), scope: "scope", retryDelays: []time.Duration{0},
				tokenProvider: func(context.Context, string) (string, error) {
					calls.Add(1)
					return testSubscriptionJWT(test.providerTier), nil
				},
				reload: func() error { reloaded <- struct{}{}; return nil },
			}
			refresher.Start("SuperGrokPro", testSubscriptionJWT(test.initialTier))
			select {
			case <-reloaded:
			case <-time.After(2 * time.Second):
				t.Fatal("catalog was not refreshed")
			}
			if calls.Load() != test.wantCalls {
				t.Fatalf("provider calls=%d", calls.Load())
			}
			if _, ok := config.LoadModelCache("session", server.URL+"/v1/models"); !ok {
				t.Fatal("refreshed model cache was not persisted")
			}
		})
	}
}

func testSubscriptionJWT(tier int) string {
	payload, _ := json.Marshal(map[string]any{"tier": tier})
	return "header." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
}
