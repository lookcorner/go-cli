package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestAuthenticateMCPServerInProcessDedup(t *testing.T) {
	server, path := oauthTestFixture(t)
	defer server.Close()

	ctx := context.Background()
	var opens atomic.Int32
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := AuthenticateMCPServer(ctx, "linear", server.URL+"/rpc", AuthenticateOpts{
				CredentialsPath: path,
				HTTPClient:      server.Client(),
				OpenURL: func(authURL string) bool {
					opens.Add(1)
					go completeOAuthAuthorize(authURL)
					return true
				},
			})
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatalf("auth err=%v", err)
		}
	}
	if opens.Load() != 1 {
		t.Fatalf("expected one browser open, got %d", opens.Load())
	}
}

func TestAuthenticateMCPServerStorePollSkipsCallback(t *testing.T) {
	server, path := oauthTestFixture(t)
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() {
		time.Sleep(150 * time.Millisecond)
		now := time.Now().Unix()
		_ = InsertAndSave(context.Background(), path, "linear", server.URL+"/rpc", StoredCredentials{
			ClientID: "other",
			TokenResponse: &TokenResponse{
				AccessToken: "from-other-process", TokenType: "bearer",
			},
			TokenReceivedAt: &now,
		})
	}()

	result, err := AuthenticateMCPServer(ctx, "linear", server.URL+"/rpc", AuthenticateOpts{
		CredentialsPath: path,
		HTTPClient:      server.Client(),
		Force:           true,
		OpenURL:         func(string) bool { return true },
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Credentials.AccessToken() != "from-other-process" {
		t.Fatalf("result=%#v", result.Credentials)
	}
}

func oauthTestFixture(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mcp_credentials.json")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		base := "http://" + r.Host
		switch {
		case strings.HasPrefix(r.URL.Path, "/.well-known/oauth-protected-resource"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"resource": base + "/rpc", "authorization_servers": []string{base},
			})
		case r.URL.Path == "/.well-known/oauth-authorization-server":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"issuer": base, "authorization_endpoint": base + "/authorize",
				"token_endpoint": base + "/token", "registration_endpoint": base + "/register",
				"scopes_supported": []string{"mcp"},
			})
		case r.URL.Path == "/register" && r.Method == http.MethodPost:
			_ = json.NewEncoder(w).Encode(map[string]any{"client_id": "dcr-client"})
		case r.URL.Path == "/authorize":
			redirect := r.URL.Query().Get("redirect_uri")
			state := r.URL.Query().Get("state")
			http.Redirect(w, r, redirect+"?code=auth-code&state="+state, http.StatusFound)
		case r.URL.Path == "/token" && r.Method == http.MethodPost:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "access-token", "token_type": "bearer", "expires_in": 3600,
				"refresh_token": "refresh-token", "scope": "mcp",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	return server, path
}

func completeOAuthAuthorize(authURL string) {
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Get(authURL)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	loc := resp.Header.Get("Location")
	if loc == "" {
		return
	}
	resp2, err := http.Get(loc)
	if err == nil {
		resp2.Body.Close()
	}
}
