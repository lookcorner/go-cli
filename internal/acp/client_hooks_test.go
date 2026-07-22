package acp

import (
	"context"
	"encoding/json"
	"io"
	"strconv"
	"testing"
	"time"

	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/api"
	"github.com/lookcorner/go-cli/internal/hooks"
	"github.com/lookcorner/go-cli/internal/tools"
	"github.com/lookcorner/go-cli/internal/workspace"
)

func TestParseClientHooksFiltersMalformedGroupsAndCapsTimeout(t *testing.T) {
	groups := parseClientHooks(map[string]any{"x.ai/hooks": map[string]any{
		"PreToolUse": []any{
			map[string]any{"matcher": "write_file", "hookCallbackIds": []any{"gate"}, "timeout": 900.0},
			map[string]any{"matcher": "[", "hookCallbackIds": []any{"bad"}},
			map[string]any{"hookCallbackIds": []any{}},
		},
		"post_tool_use": []any{map[string]any{"hookCallbackIds": []any{"observe"}, "timeout": -1.0}},
		"Unknown":       []any{map[string]any{"hookCallbackIds": []any{"ignored"}}},
	}})
	byEvent := make(map[hooks.Event]hooks.ClientHookGroup, len(groups))
	for _, group := range groups {
		byEvent[group.Event] = group
	}
	pre, post := byEvent[hooks.PreToolUse], byEvent[hooks.PostToolUse]
	if len(groups) != 2 || len(pre.CallbackIDs) != 1 || pre.Timeout != 300*time.Second || pre.CallbackIDs[0] != "gate" || len(post.CallbackIDs) != 1 || post.Timeout != 0 || post.CallbackIDs[0] != "observe" {
		t.Fatalf("groups=%#v", groups)
	}
}

func TestClientHookReverseRequestAndObserveNotification(t *testing.T) {
	group, _ := hooks.NewClientHookGroup("PreToolUse", "", []string{"gate"}, time.Second)
	observe, _ := hooks.NewClientHookGroup("PostToolUse", "", []string{"observer"}, 0)
	output := &clientHookWriter{messages: make(chan map[string]any, 4)}
	server := &Server{output: output, pendingHook: make(map[string]chan clientHookResult)}
	runner := &agent.Runner{Model: "fixture"}
	server.attachClientHooks(runner, []hooks.ClientHookGroup{group, observe}, "/sessions/s.jsonl", "/work", "session-1")
	runtime, ok := runner.HookPolicy.(*hooks.Runtime)
	if !ok || runner.HookCatalog == nil || runtime.TranscriptPath != "/sessions/s.jsonl" {
		t.Fatalf("runner=%#v policy=%T", runner, runner.HookPolicy)
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- runtime.BeforeTool(hooks.WithPromptID(context.Background(), "prompt-1"), api.ToolCall{CallID: "call-1", Name: "shell", Arguments: json.RawMessage(`{}`)})
	}()
	request := <-output.messages
	params := request["params"].(map[string]any)
	if request["method"] != "x.ai/hooks/run" || params["hookCallbackId"] != "gate" || params["sessionId"] != "session-1" || params["promptId"] != "prompt-1" || params["transcriptPath"] != "/sessions/s.jsonl" {
		t.Fatalf("request=%#v", request)
	}
	id := request["id"].(string)
	server.handleClientResponse(message{ID: json.RawMessage(strconv.Quote(id)), Result: json.RawMessage(`{"decision":"deny","systemMessage":"blocked by client"}`)})
	if err := <-errCh; err == nil || err.Error() != "hook client:gate denied tool use: blocked by client" {
		t.Fatalf("gate error=%v", err)
	}

	runtime.AfterTool(context.Background(), api.ToolCall{CallID: "call-1", Name: "shell", Arguments: json.RawMessage(`{}`)}, tools.ExecutionResult{Output: "done"}, nil)
	notification := <-output.messages
	if notification["method"] != "x.ai/hooks/event" || notification["id"] != nil || notification["params"].(map[string]any)["hookCallbackId"] != "observer" {
		t.Fatalf("notification=%#v", notification)
	}
}

func TestClientHookMalformedResponseFailsOpen(t *testing.T) {
	output := &clientHookWriter{messages: make(chan map[string]any, 1)}
	server := &Server{output: output, pendingHook: make(map[string]chan clientHookResult)}
	result := make(chan string, 1)
	go func() {
		decision, _ := server.dispatchClientHook(context.Background(), "gate", map[string]any{"sessionId": "s"}, true)
		result <- decision
	}()
	request := <-output.messages
	id := request["id"].(string)
	server.handleClientResponse(message{ID: json.RawMessage(strconv.Quote(id)), Result: json.RawMessage(`{"decision":123}`)})
	if decision := <-result; decision != "" {
		t.Fatalf("decision=%q", decision)
	}
}

func TestNewSessionRegistersClientHooksFromMeta(t *testing.T) {
	root := t.TempDir()
	output := &clientHookWriter{messages: make(chan map[string]any, 4)}
	server := &Server{
		SessionDir: t.TempDir(), output: output, sessions: make(map[string]*session), pendingHook: make(map[string]chan clientHookResult),
		Factory: func(_ context.Context, cfg SessionConfig, approver tools.Approver, _, _ io.Writer) (*agent.Runner, func(), error) {
			ws, err := workspace.Open(cfg.CWD)
			if err != nil {
				return nil, nil, err
			}
			registry := tools.NewRegistry(ws, approver)
			return &agent.Runner{Tools: registry, Model: "fixture"}, func() { _ = registry.Close() }, nil
		},
	}
	params := map[string]any{
		"cwd": root, "mcpServers": []any{},
		"_meta": map[string]any{"x.ai/hooks": map[string]any{
			"SessionStart": []any{map[string]any{"hookCallbackIds": []any{"started"}}},
			"PreToolUse":   []any{map[string]any{"matcher": "shell", "hookCallbackIds": []any{"registered"}}},
		}},
	}
	server.handleNewSession(context.Background(), message{ID: json.RawMessage("1"), Params: mustJSON(t, params)})
	started := <-output.messages
	startedParams := started["params"].(map[string]any)
	if started["method"] != "x.ai/hooks/event" || startedParams["hookCallbackId"] != "started" || startedParams["source"] != "new" {
		t.Fatalf("session start=%#v", started)
	}
	roster := <-output.messages
	if roster["method"] != "x.ai/sessions/changed" {
		t.Fatalf("roster update=%#v", roster)
	}
	created := <-output.messages
	sessionID := created["result"].(map[string]any)["sessionId"].(string)
	current := server.lookupSession(sessionID)
	if current == nil {
		t.Fatalf("session response=%#v", created)
	}
	if roster["params"].(map[string]any)["upserted"].([]any)[0].(map[string]any)["sessionId"] != sessionID {
		t.Fatalf("roster update=%#v", roster)
	}
	defer current.close()
	current.runner.StartHooks(context.Background())
	select {
	case duplicate := <-output.messages:
		t.Fatalf("duplicate session start=%#v", duplicate)
	default:
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- current.runner.HookPolicy.BeforeTool(context.Background(), api.ToolCall{Name: "shell"})
	}()
	request := <-output.messages
	if request["method"] != "x.ai/hooks/run" || request["params"].(map[string]any)["hookCallbackId"] != "registered" {
		t.Fatalf("request=%#v", request)
	}
	id := request["id"].(string)
	server.handleClientResponse(message{ID: json.RawMessage(strconv.Quote(id)), Result: json.RawMessage(`{"decision":"continue"}`)})
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
}

type clientHookWriter struct {
	messages chan map[string]any
}

func (w *clientHookWriter) Write(data []byte) (int, error) {
	var message map[string]any
	if err := json.Unmarshal(data, &message); err != nil {
		return 0, err
	}
	w.messages <- message
	return len(data), nil
}
