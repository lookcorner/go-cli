package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestAuthenticateMCPServerDCRAndPKCE(t *testing.T) {
	var registered atomic.Bool
	var sawVerifier atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		base := "http://" + r.Host
		switch {
		case strings.HasPrefix(r.URL.Path, "/.well-known/oauth-protected-resource"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"resource":              base + "/rpc",
				"authorization_servers": []string{base},
			})
		case r.URL.Path == "/.well-known/oauth-authorization-server":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"issuer":                 base,
				"authorization_endpoint": base + "/authorize",
				"token_endpoint":         base + "/token",
				"registration_endpoint":  base + "/register",
				"scopes_supported":       []string{"mcp"},
			})
		case r.URL.Path == "/register" && r.Method == http.MethodPost:
			registered.Store(true)
			_ = json.NewEncoder(w).Encode(map[string]any{"client_id": "dcr-client", "client_secret": ""})
		case r.URL.Path == "/authorize":
			http.Error(w, "browser should not hit authorize in this test", http.StatusBadRequest)
		case r.URL.Path == "/token" && r.Method == http.MethodPost:
			_ = r.ParseForm()
			if r.Form.Get("grant_type") != "authorization_code" || r.Form.Get("code") != "auth-code" {
				t.Fatalf("token form=%#v", r.Form)
			}
			if r.Form.Get("code_verifier") == "" || r.Form.Get("client_id") != "dcr-client" {
				t.Fatalf("missing PKCE/client: %#v", r.Form)
			}
			sawVerifier.Store(true)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "at-enroll", "token_type": "bearer", "expires_in": 3600, "refresh_token": "rt-enroll",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	mcpURL := server.URL + "/rpc"
	credsPath := filepath.Join(t.TempDir(), "mcp_credentials.json")
	ctx := context.Background()

	resultCh := make(chan AuthenticateResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := AuthenticateMCPServer(ctx, "linear", mcpURL, AuthenticateOpts{
			CredentialsPath: credsPath,
			HTTPClient:      server.Client(),
			Force:           true,
			OpenURL: func(authURL string) bool {
				parsed, err := url.Parse(authURL)
				if err != nil {
					errCh <- err
					return false
				}
				query := parsed.Query()
				redirect := query.Get("redirect_uri")
				state := query.Get("state")
				challenge := query.Get("code_challenge")
				if redirect == "" || state == "" || challenge == "" || query.Get("code_challenge_method") != "S256" {
					errCh <- errors.New("bad authorize URL: " + authURL)
					return false
				}
				resp, err := http.Get(redirect + "?code=auth-code&state=" + url.QueryEscape(state) + "&iss=" + url.QueryEscape(server.URL))
				if err != nil {
					errCh <- err
					return false
				}
				_, _ = io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				return true
			},
		})
		if err != nil {
			errCh <- err
			return
		}
		resultCh <- result
	}()

	select {
	case err := <-errCh:
		t.Fatal(err)
	case result := <-resultCh:
		if result.Credentials.AccessToken() != "at-enroll" || result.TokenURL != server.URL+"/token" {
			t.Fatalf("result=%#v", result)
		}
	}
	if !registered.Load() || !sawVerifier.Load() {
		t.Fatalf("registered=%v verifier=%v", registered.Load(), sawVerifier.Load())
	}
	store, err := LoadCredentialStore(credsPath)
	if err != nil {
		t.Fatal(err)
	}
	creds, ok := store.Get("linear", mcpURL)
	if !ok || creds.AccessToken() != "at-enroll" || creds.ClientID != "dcr-client" {
		t.Fatalf("store=%#v ok=%v", creds, ok)
	}
}

func TestNeedsMCPAuth(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp_credentials.json")
	stdio := ServerConfig{Name: "local", Command: "cmd"}
	if NeedsMCPAuth(stdio, path) {
		t.Fatal("stdio should not need OAuth")
	}
	remote := ServerConfig{Name: "remote", URL: "https://mcp.example/rpc"}
	if !NeedsMCPAuth(remote, path) {
		t.Fatal("remote without creds should need OAuth")
	}
	_ = SaveCredentialStore(path, CredentialStore{entries: map[string]StoredCredentials{
		CredentialKey("remote", "https://mcp.example/rpc"): {
			ClientID: "c", TokenResponse: &TokenResponse{AccessToken: "at"},
		},
	}})
	if NeedsMCPAuth(remote, path) {
		t.Fatal("remote with token should not need OAuth")
	}
	static := ServerConfig{Name: "remote", URL: "https://mcp.example/rpc", Headers: map[string]string{"Authorization": "Bearer x"}}
	if NeedsMCPAuth(static, path) {
		t.Fatal("static Authorization should skip OAuth need")
	}
}
