package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lookcorner/go-cli/internal/version"
)

func TestLoadModelCacheValidatesAndAppliesConfiguredOverrides(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	writeModelCache(t, home, modelCacheFile{
		FetchedAt: time.Now(), Version: version.Current,
		AuthMethod: "api_key", Origin: "https://api.x.ai/v1/models",
		Models: map[string]modelCacheEntry{
			"fast": {Info: modelCacheInfo{
				Model: "fast-api", BaseURL: "https://session.example/v1", Name: "Fast", Backend: "responses", ContextWindow: 1000,
				SupportedInAPI: boolPointer(false), ReasoningEfforts: []ReasoningEffortOption{{ID: "high", Value: "high", Default: true}},
			}, APIBaseURL: "https://api-key.example/v1"},
			"messages": {Info: modelCacheInfo{Model: "claude", BaseURL: "https://messages.example/v1", Backend: "messages", ContextWindow: 2000}},
		},
	})

	cache, ok := LoadModelCache("api_key", "https://api.x.ai/v1/models")
	if !ok {
		t.Fatal("fresh matching model cache was rejected")
	}
	contextWindow := 3000
	cfg := Config{ModelProfiles: map[string]ModelProfile{
		"fast":  {Name: "Configured Fast", Hidden: false, hiddenConfigured: true, ContextWindow: contextWindow},
		"local": {Model: "local-api", BaseURL: "https://local.example/v1", Backend: "responses", ContextWindow: 4000},
	}}
	cfg.ApplyModelCache(cache)

	fast := cfg.ModelProfiles["fast"]
	if fast.Model != "fast-api" || fast.Name != "Configured Fast" || fast.Hidden || fast.BaseURL != "https://api-key.example/v1" || fast.ContextWindow != 3000 || fast.ReasoningEffort != "high" || !fast.SupportsReasoningEffort {
		t.Fatalf("merged fast profile=%#v", fast)
	}
	if messages := cfg.ModelProfiles["messages"]; messages.Backend != "anthropic_messages" || messages.Hidden {
		t.Fatalf("messages profile=%#v", messages)
	}
	if local := cfg.ModelProfiles["local"]; local.Model != "local-api" {
		t.Fatalf("configured-only profile=%#v", local)
	}
}

func TestLoadModelCacheRejectsUnsafeOrStaleFiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	valid := modelCacheFile{
		FetchedAt: time.Now(), Version: version.Current, AuthMethod: "session", Origin: "https://proxy.example/v1/models",
		Models: map[string]modelCacheEntry{"model": {Info: modelCacheInfo{Model: "model", BaseURL: "https://proxy.example/v1", Backend: "responses", ContextWindow: 1000}}},
	}
	tests := []struct {
		name   string
		mutate func(*modelCacheFile)
	}{
		{name: "stale", mutate: func(cache *modelCacheFile) { cache.FetchedAt = time.Now().Add(-time.Hour) }},
		{name: "future", mutate: func(cache *modelCacheFile) { cache.FetchedAt = time.Now().Add(time.Minute) }},
		{name: "version", mutate: func(cache *modelCacheFile) { cache.Version = "other" }},
		{name: "auth", mutate: func(cache *modelCacheFile) { cache.AuthMethod = "api_key" }},
		{name: "origin", mutate: func(cache *modelCacheFile) { cache.Origin = "https://other.example/v1/models" }},
		{name: "missing origin", mutate: func(cache *modelCacheFile) { cache.Origin = "" }},
		{name: "missing models", mutate: func(cache *modelCacheFile) { cache.Models = nil }},
		{name: "invalid model", mutate: func(cache *modelCacheFile) { cache.Models["model"] = modelCacheEntry{} }},
		{name: "invalid backend", mutate: func(cache *modelCacheFile) {
			entry := cache.Models["model"]
			entry.Info.Backend = "unknown"
			cache.Models["model"] = entry
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cache := valid
			cache.Models = cloneModelCacheEntries(valid.Models)
			test.mutate(&cache)
			writeModelCache(t, home, cache)
			if _, ok := LoadModelCache("session", "https://proxy.example/v1/models"); ok {
				t.Fatal("invalid cache was accepted")
			}
		})
	}
}

func TestModelCachePathUsesGrokHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	path, err := ModelCachePath()
	if err != nil || path != filepath.Join(home, "models_cache.json") {
		t.Fatalf("path=%q err=%v", path, err)
	}
}

func TestFetchModelCacheSessionCatalog(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/models" || request.Method != http.MethodGet {
			t.Errorf("request=%s %s", request.Method, request.URL.Path)
		}
		for name, want := range map[string]string{
			"Authorization":            "Bearer session-token",
			"X-XAI-Token-Auth":         "token-header",
			"x-userid":                 "user-1",
			"x-email":                  "user@example.com",
			"x-grok-client-version":    version.Current,
			"x-grok-client-identifier": "gork-go",
			"x-grok-client-mode":       "interactive",
		} {
			if got := request.Header.Get(name); got != want {
				t.Errorf("header %s=%q want %q", name, got, want)
			}
		}
		writer.Header().Set("ETag", `"catalog-1"`)
		fmt.Fprint(writer, `{"data":[null,7,{"id":"grok-fast","model":"grok-fast-api","name":"Fast","baseUrl":"https://inference.example/v1","apiBackend":"responses","contextWindow":131072,"autoCompactThresholdPercent":80,"reasoningEffort":"MAX","reasoningEfforts":["low",{"id":"deep","value":"high","default":true},7],"supportedInApi":false},{"api_backend":"messages","_meta":{"model":"grok-meta","totalContextTokens":64000,"supportsReasoningEffort":true}},{"model":"dual-context","contextWindow":1.5,"context_window":4096}]}`)
	}))
	defer server.Close()

	cache, err := FetchModelCache(context.Background(), ModelFetchRequest{
		AuthMethod: "session", Origin: server.URL + "/models", InferenceBaseURL: server.URL + "/v1",
		Token: "session-token", TokenHeader: "token-header", UserID: "user-1", Email: "user@example.com", HTTP: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	fast := cache.profiles["grok-fast"]
	if fast.Model != "grok-fast-api" || fast.Backend != "responses" || fast.ContextWindow != 131072 || fast.ReasoningEffort != "xhigh" || !fast.SupportsReasoningEffort || len(fast.ReasoningEfforts) != 2 {
		t.Fatalf("fast profile=%#v", fast)
	}
	meta := cache.profiles["grok-meta"]
	if meta.Model != "grok-meta" || meta.Backend != "anthropic_messages" || meta.BaseURL != server.URL+"/v1" || meta.ContextWindow != 64000 || !meta.SupportsReasoningEffort {
		t.Fatalf("meta profile=%#v", meta)
	}
	if dual := cache.profiles["dual-context"]; dual.ContextWindow != 4096 {
		t.Fatalf("dual context profile=%#v", dual)
	}
	loaded, ok := LoadModelCache("session", server.URL+"/models")
	if !ok || len(loaded.profiles) != 3 {
		t.Fatalf("persisted cache loaded=%v profiles=%#v", ok, loaded.profiles)
	}
	data, err := os.ReadFile(filepath.Join(home, "models_cache.json"))
	if err != nil || !strings.Contains(string(data), `"etag": "\"catalog-1\""`) {
		t.Fatalf("cache=%q err=%v", data, err)
	}
	info, err := os.Stat(filepath.Join(home, "models_cache.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("cache mode=%v", info.Mode().Perm())
	}
}

func TestFetchModelCacheAPIKeyFallbackAndInvalidEntries(t *testing.T) {
	t.Setenv("GROK_HOME", t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if got := request.Header.Get("Authorization"); got != "Bearer api-key" {
			t.Errorf("authorization=%q", got)
		}
		if got := request.Header.Get("X-XAI-Token-Auth"); got != "" {
			t.Errorf("unexpected token auth header=%q", got)
		}
		fmt.Fprint(writer, `{"data":[null,"bad",{"name":"missing-model","contextWindow":1.5},{"id":"zero","model":"zero","contextWindow":0},{"id":"valid","model":"valid-api","apiBackend":"unknown","reasoningEffort":"extreme","reasoningEfforts":[null,"invalid",{"value":"medium","default":true}]}]}`)
	}))
	defer server.Close()

	cache, err := FetchModelCache(context.Background(), ModelFetchRequest{
		AuthMethod: "api_key", Origin: server.URL + "/models", InferenceBaseURL: server.URL + "/v1/", Token: "api-key", HTTP: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(cache.profiles) != 1 {
		t.Fatalf("profiles=%#v", cache.profiles)
	}
	profile := cache.profiles["valid"]
	if profile.BaseURL != server.URL+"/v1" || profile.Backend != "chat_completions" || profile.ContextWindow != defaultContextSize || profile.ReasoningEffort != "medium" || len(profile.ReasoningEfforts) != 1 {
		t.Fatalf("profile=%#v", profile)
	}
}

func TestFetchModelCacheFailurePreservesExistingCache(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	path := filepath.Join(home, "models_cache.json")
	original := []byte("existing-cache")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		body string
		code int
	}{
		{name: "empty", body: `{"data":[]}`},
		{name: "all invalid", body: `{"data":[null,{"name":"missing"}]}`},
		{name: "malformed", body: `{`},
		{name: "non success", body: `unavailable`, code: http.StatusServiceUnavailable},
		{name: "oversized", body: strings.Repeat("x", maxModelCacheBytes+1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				if test.code != 0 {
					writer.WriteHeader(test.code)
				}
				_, _ = io.WriteString(writer, test.body)
			}))
			defer server.Close()
			_, err := FetchModelCache(context.Background(), ModelFetchRequest{AuthMethod: "deployment", Origin: server.URL, InferenceBaseURL: server.URL, Token: "key", HTTP: server.Client()})
			if err == nil {
				t.Fatal("invalid response was accepted")
			}
			data, readErr := os.ReadFile(path)
			if readErr != nil || string(data) != string(original) {
				t.Fatalf("cache=%q readErr=%v fetchErr=%v", data, readErr, err)
			}
		})
	}
}

func TestClearModelCache(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	path := filepath.Join(home, "models_cache.json")
	if err := os.WriteFile(path, []byte("cache"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ClearModelCache(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cache still exists: %v", err)
	}
	if err := ClearModelCache(); err != nil {
		t.Fatalf("clear missing cache: %v", err)
	}
}

func writeModelCache(t *testing.T, home string, cache modelCacheFile) {
	t.Helper()
	data, err := json.Marshal(cache)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "models_cache.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func cloneModelCacheEntries(source map[string]modelCacheEntry) map[string]modelCacheEntry {
	cloned := make(map[string]modelCacheEntry, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}
