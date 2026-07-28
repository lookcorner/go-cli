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
	"github.com/lookcorner/go-cli/internal/tools"
	"github.com/lookcorner/go-cli/internal/workspace"
)

func TestLoadChatHistoryUnavailable(t *testing.T) {
	t.Setenv("GROK_SESSION_LIST_CONVERSATIONS", "true")
	authPath := filepath.Join(t.TempDir(), "auth.json")
	if err := auth.Save(authPath, "chat-scope", auth.Credential{
		Key: "oauth-token", UserID: "user-1", AuthMode: "oidc", Issuer: "https://auth.x.ai",
	}); err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer upstream.Close()
	t.Setenv("GROK_CONVERSATIONS_BASE_URL", upstream.URL)

	var output bytes.Buffer
	server := &Server{
		SessionDir: t.TempDir(), output: &output, sessions: make(map[string]*session),
		Auth: AuthConfig{Path: authPath, Scope: "chat-scope", HTTP: upstream.Client()},
	}
	server.handleLoadChatHistory(message{
		ID: json.RawMessage("9"), Method: "x.ai/session/load_history",
		Params: json.RawMessage(`{"sessionId":"conv-1"}`),
	})
	var response map[string]any
	if err := json.NewDecoder(&output).Decode(&response); err != nil {
		t.Fatal(err)
	}
	result, _ := response["result"].(map[string]any)
	meta, _ := result["_meta"].(map[string]any)
	partial, _ := meta["x.ai/partial"].(map[string]any)
	if partial["messages"] != true || partial["reason"] != "unavailable" {
		t.Fatalf("result=%#v", result)
	}
}

func TestRestoreChatSessionMarksMessagesPartial(t *testing.T) {
	t.Setenv("GROK_SESSION_LIST_CONVERSATIONS", "true")
	authPath := filepath.Join(t.TempDir(), "auth.json")
	if err := auth.Save(authPath, "chat-scope", auth.Credential{
		Key: "oauth-token", UserID: "user-1", AuthMode: "oidc", Issuer: "https://auth.x.ai",
	}); err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/rest/modes":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"modes": []map[string]any{{
					"id": "auto", "name": "Auto", "modelId": "auto",
				}},
				"defaultModeId": "auto",
			})
		case r.URL.Path == "/rest/app-chat/conversations/conv-partial/messages":
			w.WriteHeader(http.StatusNotImplemented)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()
	t.Setenv("GROK_CONVERSATIONS_BASE_URL", upstream.URL)
	t.Setenv("GROK_MODES_BASE_URL", upstream.URL)

	cwd, sessionDir := t.TempDir(), t.TempDir()
	var output bytes.Buffer
	server := &Server{
		SessionDir: sessionDir, output: &output, sessions: make(map[string]*session),
		Auth: AuthConfig{Path: authPath, Scope: "chat-scope", HTTP: upstream.Client()},
		Factory: func(_ context.Context, cfg SessionConfig, approver tools.Approver, _, _ io.Writer) (*agent.Runner, func(), error) {
			ws, err := workspace.Open(cfg.CWD)
			if err != nil {
				return nil, nil, err
			}
			registry := tools.NewRegistry(ws, approver)
			return &agent.Runner{Tools: registry, ModelID: cfg.Model}, func() { _ = registry.Close() }, nil
		},
	}
	params, err := json.Marshal(map[string]any{
		"sessionId": "conv-partial", "cwd": cwd, "mcpServers": []any{},
		"_meta": map[string]any{"x.ai/session": map[string]any{"kind": "chat"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	server.handleRestoreSession(context.Background(), message{
		ID: json.RawMessage("1"), Method: "session/load", Params: params,
	}, true)
	var result map[string]any
	for _, msg := range decodeACPOutput(t, output.Bytes()) {
		if msg["id"] == nil {
			continue
		}
		result, _ = msg["result"].(map[string]any)
		if result != nil {
			break
		}
	}
	if result == nil {
		t.Fatalf("missing load response in %q", output.String())
	}
	meta, _ := result["_meta"].(map[string]any)
	partial, _ := meta["x.ai/partial"].(map[string]any)
	sessionMeta, _ := meta["x.ai/session"].(map[string]any)
	if partial["messages"] != true || partial["reason"] != "unavailable" {
		t.Fatalf("partial=%#v meta=%#v", partial, meta)
	}
	if sessionMeta["historyLoaded"] != false {
		t.Fatalf("sessionMeta=%#v", sessionMeta)
	}
	_ = server.closeSession("conv-partial")
}
