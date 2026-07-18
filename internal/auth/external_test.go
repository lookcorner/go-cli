package auth

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseExternalCredential(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	bare, err := parseExternalCredential(" bare-token\n", time.Hour, now)
	if err != nil || bare.Key != "bare-token" || bare.AuthMode != "external" || bare.ExpiresAt == nil || !bare.ExpiresAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("bare credential=%#v err=%v", bare, err)
	}
	jsonCredential, err := parseExternalCredential(`{"access_token":"json-token","refresh_token":"refresh","expires_in":120}`, 0, now)
	if err != nil || jsonCredential.Key != "json-token" || jsonCredential.RefreshToken != "refresh" || jsonCredential.ExpiresAt == nil || !jsonCredential.ExpiresAt.Equal(now.Add(2*time.Minute)) {
		t.Fatalf("JSON credential=%#v err=%v", jsonCredential, err)
	}
	if _, err := parseExternalCredential(`{"refresh_token":"missing-access"}`, 0, now); err == nil {
		t.Fatal("JSON without access_token was accepted")
	}
	if malformed, err := parseExternalCredential("{not-json}", 0, now); err != nil || malformed.Key != "{not-json}" {
		t.Fatalf("malformed JSON fallback=%#v err=%v", malformed, err)
	}
}

func TestExternalProviderPersistsAndRefreshesRejectedToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(path, []byte(`{"scope":{"key":"stale","auth_mode":"oidc","refresh_token":"old","expires_at":"2020-01-01T00:00:00Z","oidc_issuer":"https://old.example","team_name":"Core"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	provider := ExternalProvider{
		Command: `printf 'signing in' >&2; if [ "$GROK_AUTH_EXPIRED" = 1 ]; then printf refreshed; else printf initial; fi`,
		Path:    path, Scope: "scope", Stderr: &stderr,
	}
	token, err := provider.Resolve(context.Background(), "stale")
	if err != nil || token != "refreshed" {
		t.Fatalf("resolved token=%q err=%v", token, err)
	}
	credential, err := Load(path, "scope")
	if err != nil || credential.AuthMode != "external" || credential.RefreshToken != "" || credential.ExpiresAt != nil || credential.Issuer != "" {
		t.Fatalf("persisted credential=%#v err=%v", credential, err)
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), `"team_name": "Core"`) || strings.Contains(string(data), "oidc_issuer") {
		t.Fatalf("persisted store=%s err=%v", data, err)
	}
	if stderr.String() != "signing in" {
		t.Fatalf("provider stderr=%q", stderr.String())
	}
	if token, err := provider.Resolve(context.Background(), ""); err != nil || token != "refreshed" {
		t.Fatalf("cached token=%q err=%v", token, err)
	}
}
