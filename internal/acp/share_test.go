package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/auth"
	sessionlog "github.com/lookcorner/go-cli/internal/session"
	"github.com/lookcorner/go-cli/internal/tools"
)

func TestShareSessionExtension(t *testing.T) {
	sessionDir := t.TempDir()
	logger, err := sessionlog.NewLoggerWithID(sessionDir, "share-session")
	if err != nil {
		t.Fatal(err)
	}
	if err := logger.Append("session_metadata", map[string]any{"cwd": "/workspace", "modelId": "grok-build"}); err != nil {
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

	authPath := filepath.Join(t.TempDir(), "auth.json")
	if err := auth.Save(authPath, "share-scope", auth.Credential{Key: "share-token", UserID: "user-1", Email: "user@example.com", AuthMode: "oidc", Issuer: "https://auth.x.ai"}); err != nil {
		t.Fatal(err)
	}
	var requests []struct {
		Method string
		Path   string
		Body   map[string]any
	}
	backend := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer share-token" || request.Header.Get("X-XAI-Token-Auth") != auth.DefaultTokenHeader || request.Header.Get("x-userid") != "user-1" {
			t.Errorf("headers=%#v", request.Header)
		}
		record := struct {
			Method string
			Path   string
			Body   map[string]any
		}{Method: request.Method, Path: request.URL.Path}
		if request.Body != nil {
			_ = json.NewDecoder(request.Body).Decode(&record.Body)
		}
		requests = append(requests, record)
		switch request.URL.Path {
		case "/sessions/share-session/data":
			writer.WriteHeader(http.StatusRequestEntityTooLarge)
		case "/sessions/share-session/share":
			_, _ = writer.Write([]byte(`{"permission_id":"permission-1"}`))
		default:
			writer.WriteHeader(http.StatusNoContent)
		}
	}))
	defer backend.Close()
	t.Setenv("GROK_CODE_BACKEND_URL", backend.URL)
	t.Setenv("GROK_CODE_WEB_URL", "https://web.example")

	input := bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"x.ai/share_session","params":{"session_id":"share-session"}}` + "\n")
	var output bytes.Buffer
	server := &Server{
		SessionDir: sessionDir, SharingEnabled: func() bool { return true },
		Auth: AuthConfig{Path: authPath, Scope: "share-scope", HTTP: backend.Client()},
		Factory: func(context.Context, SessionConfig, tools.Approver, io.Writer, io.Writer) (*agent.Runner, func(), error) {
			return nil, nil, nil
		},
	}
	if err := server.Serve(context.Background(), input, &output); err != nil {
		t.Fatal(err)
	}
	response := decodeACP(t, json.NewDecoder(&output))
	if response["result"].(map[string]any)["share_url"] != "https://web.example/build/share/permission-1" {
		t.Fatalf("response=%#v", response)
	}
	if len(requests) != 3 || requests[0].Method != http.MethodPut || requests[0].Path != "/sessions/share-session" || requests[1].Path != "/sessions/share-session/data" || requests[2].Path != "/sessions/share-session/share" {
		t.Fatalf("requests=%#v", requests)
	}
	if requests[0].Body["agentId"] != "gork-go" {
		t.Fatalf("upsert=%#v", requests[0].Body)
	}
	data := requests[1].Body
	messages := data["messages"].([]any)
	metadata := data["metadata"].(map[string]any)
	if len(messages) != 2 || metadata["cwd"] != "/workspace" || metadata["model_id"] != "grok-build" || metadata["total_messages"] != float64(2) {
		t.Fatalf("data=%#v", data)
	}
	first := messages[0].(map[string]any)
	if first["timestamp"] == "" || !bytes.Contains([]byte(first["content"].(string)), []byte(`"user_message_chunk"`)) {
		t.Fatalf("first message=%#v", first)
	}
}

func TestShareSessionRequiresRemoteEnablement(t *testing.T) {
	authPath := filepath.Join(t.TempDir(), "auth.json")
	if err := auth.Save(authPath, "share-scope", auth.Credential{Key: "share-token", AuthMode: "oidc", Issuer: "https://auth.x.ai"}); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	server := &Server{output: &output, Auth: AuthConfig{Path: authPath, Scope: "share-scope"}}
	server.handleShareSession(context.Background(), message{ID: json.RawMessage("1"), Params: json.RawMessage(`{"session_id":"share-session"}`)})
	response := decodeACP(t, json.NewDecoder(&output))
	if response["error"].(map[string]any)["code"] != float64(-32602) {
		t.Fatalf("response=%#v", response)
	}
}

func TestShareSessionRejectsNonXAIAndZDRCredentials(t *testing.T) {
	for _, test := range []struct {
		name       string
		credential auth.Credential
		wantCode   float64
	}{
		{name: "API key", credential: auth.Credential{Key: "key", AuthMode: "api_key"}, wantCode: -32000},
		{name: "ZDR team", credential: auth.Credential{Key: "key", AuthMode: "oidc", Issuer: "https://auth.x.ai", TeamBlockedReasons: []string{"BLOCKED_REASON_NO_LOGS"}}, wantCode: -32602},
	} {
		t.Run(test.name, func(t *testing.T) {
			authPath := filepath.Join(t.TempDir(), "auth.json")
			if err := auth.Save(authPath, "share-scope", test.credential); err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			server := &Server{output: &output, SharingEnabled: func() bool { return true }, Auth: AuthConfig{Path: authPath, Scope: "share-scope"}}
			server.handleShareSession(context.Background(), message{ID: json.RawMessage("1"), Params: json.RawMessage(`{"session_id":"share-session"}`)})
			response := decodeACP(t, json.NewDecoder(&output))
			if response["error"].(map[string]any)["code"] != test.wantCode {
				t.Fatalf("response=%#v", response)
			}
		})
	}
}
