package config

import (
	"encoding/json"
	"os"
	"path/filepath"
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
