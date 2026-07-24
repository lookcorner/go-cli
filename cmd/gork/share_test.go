package main

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/lookcorner/go-cli/internal/auth"
	"github.com/lookcorner/go-cli/internal/config"
	sessionshare "github.com/lookcorner/go-cli/internal/share"
)

func TestShareCLIRefreshesSettingsAndPrintsURL(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	authConfig := auth.DefaultConfig()
	authPath := filepath.Join(home, "auth.json")
	if err := auth.Save(authPath, authConfig.Scope(), auth.Credential{
		Key: "token", AuthMode: "oidc", Issuer: "https://auth.x.ai", UserID: "user-1", Email: "user@example.com",
	}); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(home, "config.toml")
	sessionDir := t.TempDir()
	previousFetch, previousShare := fetchShareSettings, shareSession
	fetchShareSettings = func(_ context.Context, _ string, token, userID, email string, _ *http.Client) *config.RemoteSettings {
		if token != "token" || userID != "user-1" || email != "user@example.com" {
			t.Fatalf("token=%q user=%q email=%q", token, userID, email)
		}
		enabled := true
		return &config.RemoteSettings{SharingEnabled: &enabled}
	}
	shareSession = func(_ context.Context, service sessionshare.Service, sessionID string) (string, error) {
		if sessionID != "session-1" || service.SessionDir != sessionDir || service.Enabled == nil || !service.Enabled() {
			t.Fatalf("session=%q service=%#v", sessionID, service)
		}
		return "https://grok.example/build/share/one", nil
	}
	t.Cleanup(func() { fetchShareSettings, shareSession = previousFetch, previousShare })

	var stdout, stderr bytes.Buffer
	err := runShare([]string{"session-1", "--config", configPath, "--session-dir", sessionDir}, &stdout, &stderr)
	if err != nil || stdout.String() != "https://grok.example/build/share/one\n" {
		t.Fatalf("err=%v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
}

func TestParseShareArgsSupportsFlagsBeforeAndAfterSession(t *testing.T) {
	for _, args := range [][]string{
		{"session-1", "--config", "config.toml", "--session-dir", "sessions"},
		{"--config", "config.toml", "session-1", "--session-dir", "sessions"},
	} {
		sessionID, configPath, sessionDir, help, err := parseShareArgs(args)
		if err != nil || help || sessionID != "session-1" || configPath != "config.toml" || sessionDir != "sessions" {
			t.Fatalf("args=%v session=%q config=%q dir=%q help=%v err=%v", args, sessionID, configPath, sessionDir, help, err)
		}
	}
}

func TestShareCLIRequiresSessionAndXAIAuth(t *testing.T) {
	if err := runShare(nil, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("share accepted no session ID")
	}
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "config.toml"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runShare([]string{"session-1"}, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("share accepted missing xAI auth")
	}
	authConfig := auth.DefaultConfig()
	if err := auth.Save(filepath.Join(home, "auth.json"), authConfig.Scope(), auth.Credential{
		Key: "api-key", AuthMode: "api_key",
	}); err != nil {
		t.Fatal(err)
	}
	if err := runShare([]string{"session-1"}, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("share accepted API-key auth")
	}
}

func TestShareCLIHelpDoesNotLoadConfiguration(t *testing.T) {
	t.Setenv("GROK_HOME", t.TempDir())
	var stdout, stderr bytes.Buffer
	if err := runShare([]string{"--help"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != shareUsage+"\n" || stderr.Len() != 0 {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestShareCLIFailsClosedWhenSettingsAreUnavailable(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	authConfig := auth.DefaultConfig()
	if err := auth.Save(filepath.Join(home, "auth.json"), authConfig.Scope(), auth.Credential{
		Key: "token", AuthMode: "oidc", Issuer: "https://auth.x.ai",
	}); err != nil {
		t.Fatal(err)
	}
	previousFetch, previousShare := fetchShareSettings, shareSession
	fetchShareSettings = func(context.Context, string, string, string, string, *http.Client) *config.RemoteSettings {
		return nil
	}
	called := false
	shareSession = func(_ context.Context, service sessionshare.Service, _ string) (string, error) {
		called = true
		if service.Enabled != nil && service.Enabled() {
			t.Fatal("sharing remained enabled without remote settings")
		}
		return service.Share(context.Background(), "session-1")
	}
	t.Cleanup(func() { fetchShareSettings, shareSession = previousFetch, previousShare })

	err := runShare([]string{"session-1"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || called == false {
		t.Fatalf("err=%v called=%v", err, called)
	}
}
