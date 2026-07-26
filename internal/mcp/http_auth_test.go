package mcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestStartHTTPUsesStoredBearerAndRefreshesOnce(t *testing.T) {
	var mcpHits atomic.Int32
	var refreshHits atomic.Int32

	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refreshHits.Add(1)
		_ = r.ParseForm()
		if r.Form.Get("refresh_token") != "rt-1" {
			t.Fatalf("unexpected refresh token %q", r.Form.Get("refresh_token"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "at-2", "token_type": "bearer", "expires_in": 3600, "refresh_token": "rt-2",
		})
	}))
	defer tokenServer.Close()

	mcpURL := "https://mcp.example/rpc"
	path := filepath.Join(t.TempDir(), "mcp_credentials.json")
	if err := SaveCredentialStore(path, CredentialStore{entries: map[string]StoredCredentials{
		CredentialKey("oauth", mcpURL): {
			ClientID: "client",
			TokenResponse: &TokenResponse{
				AccessToken: "at-1", TokenType: "bearer", RefreshToken: "rt-1",
			},
		},
	}}); err != nil {
		t.Fatal(err)
	}

	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() == tokenServer.URL || strings.HasPrefix(request.URL.String(), tokenServer.URL) {
			return tokenServer.Client().Transport.RoundTrip(request)
		}
		if request.Method == http.MethodDelete {
			return httpResponse(http.StatusNoContent, "", ""), nil
		}
		hit := mcpHits.Add(1)
		body, _ := io.ReadAll(request.Body)
		auth := request.Header.Get("Authorization")
		if hit == 1 {
			if auth != "Bearer at-1" {
				t.Fatalf("first request auth=%q", auth)
			}
			if len(body) == 0 {
				t.Fatal("first request body empty")
			}
			return httpResponse(http.StatusUnauthorized, "text/plain", "expired"), nil
		}
		if auth != "Bearer at-2" {
			t.Fatalf("retry auth=%q", auth)
		}
		if len(body) == 0 {
			t.Fatal("retry request body empty")
		}
		var rpc struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		if err := json.Unmarshal(body, &rpc); err != nil {
			t.Fatal(err)
		}
		switch rpc.Method {
		case "initialize":
			response := httpResponse(http.StatusOK, "application/json", rpcResult(rpc.ID, map[string]any{
				"protocolVersion": protocolVersion,
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "oauth-fixture", "version": "1"},
			}))
			response.Header.Set("Mcp-Session-Id", "sess")
			return response, nil
		case "notifications/initialized":
			return httpResponse(http.StatusAccepted, "", ""), nil
		default:
			t.Fatalf("unexpected method %s", rpc.Method)
			return nil, nil
		}
	})

	client, initialized, err := StartHTTP(context.Background(), HTTPConfig{
		Name: "oauth", URL: mcpURL,
		Client: &http.Client{Transport: transport},
		Auth:   &HTTPAuth{CredentialsPath: path, TokenURL: tokenServer.URL},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if initialized.ServerInfo.Name != "oauth-fixture" {
		t.Fatalf("initialized=%#v", initialized)
	}
	if mcpHits.Load() < 2 || refreshHits.Load() != 1 {
		t.Fatalf("mcpHits=%d refreshHits=%d", mcpHits.Load(), refreshHits.Load())
	}
	store, err := LoadCredentialStore(path)
	if err != nil {
		t.Fatal(err)
	}
	creds, _ := store.Get("oauth", mcpURL)
	if creds.AccessToken() != "at-2" {
		t.Fatalf("persisted token=%#v", creds)
	}
}

func TestStartHTTPStaticAuthorizationSkipsStore(t *testing.T) {
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("Authorization") != "Bearer static" {
			t.Fatalf("auth=%q", request.Header.Get("Authorization"))
		}
		if request.Method == http.MethodDelete {
			return httpResponse(http.StatusNoContent, "", ""), nil
		}
		var rpc struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		if request.Body != nil {
			_ = json.NewDecoder(request.Body).Decode(&rpc)
		}
		switch rpc.Method {
		case "initialize":
			response := httpResponse(http.StatusOK, "application/json", rpcResult(rpc.ID, map[string]any{
				"protocolVersion": protocolVersion,
				"capabilities":    map[string]any{},
				"serverInfo":      map[string]any{"name": "static", "version": "1"},
			}))
			response.Header.Set("Mcp-Session-Id", "s")
			return response, nil
		case "notifications/initialized":
			return httpResponse(http.StatusAccepted, "", ""), nil
		default:
			t.Fatalf("method=%s", rpc.Method)
			return nil, nil
		}
	})
	path := filepath.Join(t.TempDir(), "mcp_credentials.json")
	_ = SaveCredentialStore(path, CredentialStore{entries: map[string]StoredCredentials{
		CredentialKey("svc", "https://mcp.example/rpc"): {
			ClientID: "c", TokenResponse: &TokenResponse{AccessToken: "store"},
		},
	}})
	client, _, err := StartHTTP(context.Background(), HTTPConfig{
		Name: "svc", URL: "https://mcp.example/rpc",
		Headers: map[string]string{"Authorization": "Bearer static"},
		Client:  &http.Client{Transport: transport},
		Auth:    &HTTPAuth{CredentialsPath: path, TokenURL: "https://auth.example/token"},
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = client.Close()
}
