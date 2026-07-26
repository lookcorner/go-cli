package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestCredentialStoreLegacyFixture(t *testing.T) {
	const fixture = `{
            "linear:https://mcp.example.com/mcp": {
                "client_id": "legacy-client-id",
                "token_response": {
                    "access_token": "at-123",
                    "token_type": "bearer",
                    "expires_in": 3600,
                    "refresh_token": "rt-456",
                    "scope": "read write"
                },
                "granted_scopes": ["read", "write"],
                "token_received_at": 1730000000
            },
            "noauth:https://example.com/mcp": {
                "client_id": "c2",
                "token_response": null
            }
        }`
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp_credentials.json")
	if err := os.WriteFile(path, []byte(fixture), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := LoadCredentialStore(path)
	if err != nil {
		t.Fatal(err)
	}
	creds, ok := store.Get("linear", "https://mcp.example.com/mcp")
	if !ok || creds.AccessToken() != "at-123" || creds.TokenResponse.RefreshToken != "rt-456" {
		t.Fatalf("unexpected creds: %#v ok=%v", creds, ok)
	}
	if creds.ClientID != "legacy-client-id" || len(creds.GrantedScopes) != 2 || creds.TokenReceivedAt == nil || *creds.TokenReceivedAt != 1730000000 {
		t.Fatalf("unexpected metadata: %#v", creds)
	}
	empty, ok := store.Get("noauth", "https://example.com/mcp")
	if !ok || empty.AccessToken() != "" {
		t.Fatalf("unexpected empty entry: %#v ok=%v", empty, ok)
	}
}

func TestCredentialStoreSaveOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("owner-only mode is Unix-specific")
	}
	path := filepath.Join(t.TempDir(), "mcp_credentials.json")
	store := CredentialStore{entries: map[string]StoredCredentials{
		CredentialKey("test", "https://mcp.example/mcp"): {
			ClientID: "c",
			TokenResponse: &TokenResponse{
				AccessToken: "at", TokenType: "bearer", RefreshToken: "rt",
			},
		},
	}}
	if err := SaveCredentialStore(path, store); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%o want 0600", info.Mode().Perm())
	}
}

func TestApplyCredentialHeadersSkipsStaticAuthorization(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp_credentials.json")
	_ = SaveCredentialStore(path, CredentialStore{entries: map[string]StoredCredentials{
		CredentialKey("svc", "https://mcp.example/rpc"): {
			ClientID: "c", TokenResponse: &TokenResponse{AccessToken: "from-store"},
		},
	}})
	headers := ApplyCredentialHeaders(map[string]string{"Authorization": "Bearer static"}, "svc", "https://mcp.example/rpc", path)
	if headers["Authorization"] != "Bearer static" {
		t.Fatalf("static auth overridden: %#v", headers)
	}
	headers = ApplyCredentialHeaders(nil, "svc", "https://mcp.example/rpc", path)
	if headers["Authorization"] != "Bearer from-store" {
		t.Fatalf("store auth missing: %#v", headers)
	}
}

func TestRefreshStoredCredentials(t *testing.T) {
	var sawRefresh bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("refresh_token") != "rt-old" {
			t.Fatalf("unexpected form: %#v", r.Form)
		}
		sawRefresh = true
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "at-new", "token_type": "bearer", "expires_in": 3600, "refresh_token": "rt-new",
		})
	}))
	defer server.Close()

	path := filepath.Join(t.TempDir(), "mcp_credentials.json")
	received := time.Now().Add(-time.Hour).Unix()
	if err := SaveCredentialStore(path, CredentialStore{entries: map[string]StoredCredentials{
		CredentialKey("svc", "https://mcp.example/rpc"): {
			ClientID: "client-1",
			TokenResponse: &TokenResponse{
				AccessToken: "at-old", TokenType: "bearer", RefreshToken: "rt-old",
			},
			TokenReceivedAt: &received,
		},
	}}); err != nil {
		t.Fatal(err)
	}
	updated, err := RefreshStoredCredentials(context.Background(), path, "svc", "https://mcp.example/rpc", server.URL, "", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if !sawRefresh || updated.AccessToken() != "at-new" || updated.TokenResponse.RefreshToken != "rt-new" {
		t.Fatalf("unexpected refresh result: %#v saw=%v", updated, sawRefresh)
	}
	store, err := LoadCredentialStore(path)
	if err != nil {
		t.Fatal(err)
	}
	creds, _ := store.Get("svc", "https://mcp.example/rpc")
	if creds.AccessToken() != "at-new" {
		t.Fatalf("store not updated: %#v", creds)
	}
}

func TestDefaultCredentialsPathUsesGrokHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	path, err := DefaultCredentialsPath()
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(home, "mcp_credentials.json") {
		t.Fatalf("path=%q", path)
	}
}
