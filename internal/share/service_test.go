package share

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lookcorner/go-cli/internal/auth"
	"github.com/lookcorner/go-cli/internal/session"
)

func TestServiceSharesSessionAndRefreshesUnauthorizedToken(t *testing.T) {
	sessionDir := t.TempDir()
	logger, err := session.NewLoggerWithID(sessionDir, "share-session")
	if err != nil {
		t.Fatal(err)
	}
	if err := logger.Append("session_metadata", map[string]any{"cwd": "/workspace", "modelId": "grok-build", "title": "Shared turn"}); err != nil {
		t.Fatal(err)
	}
	if err := logger.AppendPrompt("hello", nil); err != nil {
		t.Fatal(err)
	}
	if err := logger.Append("model_response", map[string]any{"text": "world", "response_id": "response-1", "tool_call_count": 0}); err != nil {
		t.Fatal(err)
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	authPath, scope := filepath.Join(t.TempDir(), "auth.json"), "share-scope"
	if err := auth.Save(authPath, scope, auth.Credential{Key: "old", UserID: "user-1", Email: "user@example.com", AuthMode: "oidc", Issuer: "https://auth.x.ai"}); err != nil {
		t.Fatal(err)
	}
	requests, refreshed := 0, 0
	backend := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		if requests == 1 {
			if request.Header.Get("Authorization") != "Bearer old" {
				t.Errorf("first authorization=%q", request.Header.Get("Authorization"))
			}
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		if request.Header.Get("Authorization") != "Bearer new" || request.Header.Get("x-userid") != "user-1" || request.Header.Get("x-email") != "user@example.com" {
			t.Errorf("headers=%#v", request.Header)
		}
		switch request.URL.Path {
		case "/sessions/share-session":
			var body map[string]any
			if json.NewDecoder(request.Body).Decode(&body) != nil || body["agentId"] != "gork-go" {
				t.Errorf("upsert=%#v", body)
			}
			writer.WriteHeader(http.StatusNoContent)
		case "/sessions/share-session/data":
			writer.WriteHeader(http.StatusRequestEntityTooLarge)
		case "/sessions/share-session/share":
			_, _ = writer.Write([]byte(`{"permission_id":"permission-1"}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer backend.Close()
	service := Service{
		SessionDir: sessionDir, AuthPath: authPath, AuthScope: scope, HTTP: backend.Client(),
		BackendURL: backend.URL, WebURL: "https://web.example", Enabled: func() bool { return true },
		TokenProvider: func(_ context.Context, rejected string) (string, error) {
			refreshed++
			if rejected != "old" {
				t.Fatalf("rejected token=%q", rejected)
			}
			return "new", nil
		},
	}
	url, err := service.Share(context.Background(), "share-session")
	if err != nil || url != "https://web.example/build/share/permission-1" || requests != 4 || refreshed != 1 {
		t.Fatalf("url=%q err=%v requests=%d refreshed=%d", url, err, requests, refreshed)
	}
}

func TestServiceRejectsUnavailableSharingStates(t *testing.T) {
	authPath, scope := filepath.Join(t.TempDir(), "auth.json"), "share-scope"
	assertKind := func(service Service, sessionID string, want ErrorKind, text string) {
		t.Helper()
		_, err := service.Share(context.Background(), sessionID)
		var shareErr *Error
		if !errors.As(err, &shareErr) || shareErr.Kind != want || !strings.Contains(shareErr.Message, text) {
			t.Fatalf("session=%q error=%#v", sessionID, err)
		}
	}
	service := Service{AuthPath: authPath, AuthScope: scope, Enabled: func() bool { return true }}
	assertKind(service, "", Invalid, "session_id")
	assertKind(service, "missing", Authentication, "gork login")

	if err := auth.Save(authPath, scope, auth.Credential{Key: "key", AuthMode: "api_key"}); err != nil {
		t.Fatal(err)
	}
	assertKind(service, "missing", Authentication, "gork login")
	if err := auth.Save(authPath, scope, auth.Credential{Key: "key", AuthMode: "oidc", Issuer: "https://auth.x.ai"}); err != nil {
		t.Fatal(err)
	}
	service.Enabled = func() bool { return false }
	assertKind(service, "missing", Invalid, "not available")
	service.Enabled = func() bool { return true }
	assertKind(service, "missing", NotFound, "Session not found")

	if err := auth.Save(authPath, scope, auth.Credential{Key: "key", AuthMode: "oidc", Issuer: "https://auth.x.ai", TeamBlockedReasons: []string{"BLOCKED_REASON_NO_LOGS"}}); err != nil {
		t.Fatal(err)
	}
	assertKind(service, "missing", Invalid, "data retention policy")
}

func TestServiceRejectsEmptySession(t *testing.T) {
	sessionDir := t.TempDir()
	logger, err := session.NewLoggerWithID(sessionDir, "empty")
	if err != nil {
		t.Fatal(err)
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	authPath, scope := filepath.Join(t.TempDir(), "auth.json"), "share-scope"
	if err := auth.Save(authPath, scope, auth.Credential{Key: "key", AuthMode: "oidc", Issuer: "https://auth.x.ai"}); err != nil {
		t.Fatal(err)
	}
	service := Service{SessionDir: sessionDir, AuthPath: authPath, AuthScope: scope, Enabled: func() bool { return true }}
	_, err = service.Share(context.Background(), "empty")
	var shareErr *Error
	if !errors.As(err, &shareErr) || shareErr.Kind != Invalid || shareErr.Message != "No messages to share yet" {
		t.Fatalf("empty error=%#v", err)
	}
}

func TestServiceRejectsRemoteFailures(t *testing.T) {
	sessionDir := t.TempDir()
	logger, err := session.NewLoggerWithID(sessionDir, "share-session")
	if err != nil {
		t.Fatal(err)
	}
	if err := logger.AppendPrompt("hello", nil); err != nil {
		t.Fatal(err)
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	authPath, scope := filepath.Join(t.TempDir(), "auth.json"), "share-scope"
	if err := auth.Save(authPath, scope, auth.Credential{Key: "key", AuthMode: "oidc", Issuer: "https://auth.x.ai"}); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name          string
		handler       http.HandlerFunc
		tokenProvider func(context.Context, string) (string, error)
		want          string
	}{
		{
			name: "invalid share response",
			handler: func(writer http.ResponseWriter, request *http.Request) {
				if strings.HasSuffix(request.URL.Path, "/share") {
					_, _ = writer.Write([]byte(`{}`))
					return
				}
				writer.WriteHeader(http.StatusNoContent)
			},
			want: "invalid share response",
		},
		{
			name: "backend failure",
			handler: func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(http.StatusBadGateway)
			},
			want: "upsert session failed: HTTP 502",
		},
		{
			name: "token refresh failure",
			handler: func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(http.StatusUnauthorized)
			},
			tokenProvider: func(context.Context, string) (string, error) {
				return "", errors.New("refresh unavailable")
			},
			want: "authentication refresh failed",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			backend := httptest.NewServer(test.handler)
			defer backend.Close()
			service := Service{
				SessionDir: sessionDir, AuthPath: authPath, AuthScope: scope,
				HTTP: backend.Client(), BackendURL: backend.URL, Enabled: func() bool { return true },
				TokenProvider: test.tokenProvider,
			}
			_, err := service.Share(context.Background(), "share-session")
			var shareErr *Error
			if !errors.As(err, &shareErr) || shareErr.Kind != Internal || !strings.Contains(shareErr.Message, test.want) {
				t.Fatalf("error=%#v", err)
			}
		})
	}
}
