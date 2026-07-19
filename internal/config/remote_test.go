package config

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func boolPointer(value bool) *bool       { return &value }
func stringPointer(value string) *string { return &value }

func TestFetchRemoteSettingsRetriesAndAuthenticates(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		attempts++
		if request.URL.Path != "/v1/settings" || request.Header.Get("Authorization") != "Bearer session-token" {
			t.Fatalf("request path=%q authorization=%q", request.URL.Path, request.Header.Get("Authorization"))
		}
		if attempts == 1 {
			http.Error(writer, "temporary", http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(writer).Encode(RemoteSettings{WebFetchEnabled: boolPointer(true)})
	}))
	defer server.Close()
	settings := FetchRemoteSettings(context.Background(), server.URL+"/v1", "session-token", server.Client())
	if settings == nil || settings.WebFetchEnabled == nil || !*settings.WebFetchEnabled || attempts != 2 {
		t.Fatalf("settings=%#v attempts=%d", settings, attempts)
	}
}

func TestApplyRemoteSettingsUsesLocalAndEnvironmentPrecedence(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "config.toml")
	if err := os.WriteFile(path, []byte("[features]\nweb_fetch = false\nauto_wake = false\n[toolset.web_fetch]\nproxy_endpoint = \"https://local.example\"\n[compat.cursor]\nskills = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GROK_OFFICIAL_MARKETPLACE_AUTO_REGISTER", "false")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ApplyRemoteSettings(&RemoteSettings{
		OfficialMarketplaceAutoRegister: boolPointer(true), WebFetchEnabled: boolPointer(true), AutoWakeEnabled: boolPointer(true),
		WebFetchProxy: stringPointer("https://remote.example"), WebFetchAllowedDomains: []string{"docs.example"},
		CursorSkills: boolPointer(false), ClaudeHooks: boolPointer(false),
	})
	if cfg.OfficialMarketplaceAutoRegister || cfg.WebFetch.Enabled || cfg.AutoWakeEnabled || cfg.WebFetch.ProxyEndpoint != "https://local.example" || !cfg.Compat.Cursor.Skills || cfg.Compat.Claude.Hooks || !cfg.WebFetch.DomainsConfigured || len(cfg.WebFetch.AllowedDomains) != 1 {
		t.Fatalf("config=%#v", cfg)
	}
}

func TestAutoWakeDefaultsOnAndRemoteCanDisable(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "missing.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.AutoWakeEnabled {
		t.Fatal("auto wake should default on")
	}
	cfg.ApplyRemoteSettings(&RemoteSettings{AutoWakeEnabled: boolPointer(false)})
	if cfg.AutoWakeEnabled {
		t.Fatal("remote auto wake gate was not applied")
	}
}
