package acp

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/api"
	"github.com/lookcorner/go-cli/internal/tools"
	"github.com/lookcorner/go-cli/internal/workspace"
)

type fixtureStreamer struct {
	mu      sync.Mutex
	results []api.StreamResult
}

type blockingStreamer struct{ started chan struct{} }

func (f *blockingStreamer) StreamResponse(ctx context.Context, _ api.ResponseRequest, _ func(string)) (api.StreamResult, error) {
	close(f.started)
	<-ctx.Done()
	return api.StreamResult{}, ctx.Err()
}

func (f *fixtureStreamer) StreamResponse(ctx context.Context, _ api.ResponseRequest, onText func(string)) (api.StreamResult, error) {
	f.mu.Lock()
	result := f.results[0]
	f.results = f.results[1:]
	f.mu.Unlock()
	if result.Text != "" {
		onText(result.Text)
	}
	return result, nil
}

func TestACPStdioLifecycleStreamingAndPermission(t *testing.T) {
	root := t.TempDir()
	streamer := &fixtureStreamer{results: []api.StreamResult{
		{ResponseID: "response-1", ToolCalls: []api.ToolCall{{
			CallID: "tool-1", Name: "write_file", Arguments: json.RawMessage(`{"path":"made.txt","content":"ok"}`),
		}}},
		{ResponseID: "response-2", Text: "finished"},
	}}
	factoryConfigs := make(chan SessionConfig, 1)
	server := &Server{Factory: func(_ context.Context, cfg SessionConfig, approver tools.Approver, text, status io.Writer) (*agent.Runner, func(), error) {
		factoryConfigs <- cfg
		ws, err := workspace.Open(cfg.CWD)
		if err != nil {
			return nil, nil, err
		}
		registry := tools.NewRegistry(ws, approver)
		return &agent.Runner{Client: streamer, Tools: registry, Model: "fixture", MaxSteps: 3, TextOutput: text, StatusOutput: status}, func() { _ = registry.Close() }, nil
	}}
	clientToAgentR, clientToAgentW := io.Pipe()
	agentToClientR, agentToClientW := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- server.Serve(context.Background(), clientToAgentR, agentToClientW) }()
	encoder := json.NewEncoder(clientToAgentW)
	decoder := json.NewDecoder(agentToClientR)
	encodeACP(t, encoder, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": 1}})
	initialize := decodeACP(t, decoder)
	if int(initialize["id"].(float64)) != 1 {
		t.Fatalf("unexpected initialize response: %#v", initialize)
	}
	promptCapabilities := initialize["result"].(map[string]any)["agentCapabilities"].(map[string]any)["promptCapabilities"].(map[string]any)
	if promptCapabilities["embeddedContext"] != true || promptCapabilities["image"] != false {
		t.Fatalf("unexpected prompt capabilities: %#v", promptCapabilities)
	}
	sessionCapabilities := initialize["result"].(map[string]any)["agentCapabilities"].(map[string]any)["sessionCapabilities"].(map[string]any)
	if _, ok := sessionCapabilities["list"]; !ok {
		t.Fatalf("session list capability missing: %#v", sessionCapabilities)
	}
	encodeACP(t, encoder, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "session/new", "params": map[string]any{
		"cwd": root, "mcpServers": []any{map[string]any{
			"name": "client-tools", "command": "/fixture-mcp", "args": []string{"--stdio"},
			"env": []any{map[string]any{"name": "TOKEN", "value": "secret"}},
		}},
	}})
	created := decodeACP(t, decoder)
	receivedConfig := <-factoryConfigs
	if len(receivedConfig.MCPServers) != 1 || receivedConfig.MCPServers[0].Env["TOKEN"] != "secret" {
		t.Fatalf("client MCP config was not forwarded: %#v", receivedConfig)
	}
	sessionID := created["result"].(map[string]any)["sessionId"].(string)
	encodeACP(t, encoder, map[string]any{"jsonrpc": "2.0", "id": 22, "method": "session/list", "params": map[string]any{"cwd": root}})
	listed := decodeACP(t, decoder)
	sessions := listed["result"].(map[string]any)["sessions"].([]any)
	if len(sessions) != 1 || sessions[0].(map[string]any)["sessionId"] != sessionID {
		t.Fatalf("unexpected session list: %#v", listed)
	}
	encodeACP(t, encoder, map[string]any{"jsonrpc": "2.0", "id": 3, "method": "session/prompt", "params": map[string]any{
		"sessionId": sessionID, "prompt": []any{map[string]any{"type": "text", "text": "create the file"}},
	}})
	titleUpdate := decodeACP(t, decoder)
	infoUpdate := titleUpdate["params"].(map[string]any)["update"].(map[string]any)
	if infoUpdate["sessionUpdate"] != "session_info_update" || infoUpdate["title"] != "create the file" {
		t.Fatalf("unexpected session info update: %#v", titleUpdate)
	}
	toolStarted := decodeACP(t, decoder)
	startedUpdate := toolStarted["params"].(map[string]any)["update"].(map[string]any)
	if startedUpdate["sessionUpdate"] != "tool_call" || startedUpdate["toolCallId"] != "tool-1" {
		t.Fatalf("unexpected tool start: %#v", toolStarted)
	}
	permission := decodeACP(t, decoder)
	if permission["method"] != "session/request_permission" {
		t.Fatalf("unexpected permission request: %#v", permission)
	}
	permissionTool := permission["params"].(map[string]any)["toolCall"].(map[string]any)
	if permissionTool["toolCallId"] != "tool-1" {
		t.Fatalf("permission did not reference tool call: %#v", permission)
	}
	encodeACP(t, encoder, map[string]any{
		"jsonrpc": "2.0", "id": permission["id"],
		"result": map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": "allow_once"}},
	})
	toolFinished := decodeACP(t, decoder)
	finishedUpdate := toolFinished["params"].(map[string]any)["update"].(map[string]any)
	if finishedUpdate["sessionUpdate"] != "tool_call_update" || finishedUpdate["status"] != "completed" {
		t.Fatalf("unexpected tool finish: %#v", toolFinished)
	}
	textUpdate := decodeACP(t, decoder)
	if textUpdate["method"] != "session/update" {
		t.Fatalf("unexpected stream update: %#v", textUpdate)
	}
	completed := decodeACP(t, decoder)
	if int(completed["id"].(float64)) != 3 || completed["result"].(map[string]any)["stopReason"] != "end_turn" {
		t.Fatalf("unexpected prompt response: %#v", completed)
	}
	data, err := os.ReadFile(filepath.Join(root, "made.txt"))
	if err != nil || string(data) != "ok" {
		t.Fatalf("tool did not run: data=%q err=%v", data, err)
	}
	encodeACP(t, encoder, map[string]any{"jsonrpc": "2.0", "id": 4, "method": "session/close", "params": map[string]any{"sessionId": sessionID}})
	closed := decodeACP(t, decoder)
	if int(closed["id"].(float64)) != 4 {
		t.Fatalf("unexpected close response: %#v", closed)
	}
	_ = clientToAgentW.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ACP server did not stop at EOF")
	}
}

func TestRenderPromptSupportsEmbeddedTextAndRejectsUnsupportedMedia(t *testing.T) {
	var embedded promptBlock
	embedded.Type = "resource"
	embedded.Resource.URI = "file:///workspace/context.md"
	embedded.Resource.MimeType = "text/markdown"
	embedded.Resource.Text = "# Context"
	parts, err := renderPrompt([]promptBlock{
		{Type: "text", Text: "Use this context"},
		embedded,
		{Type: "resource_link", Name: "spec", URI: "file:///workspace/spec.md"},
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(parts, "\n\n")
	if !strings.Contains(joined, "Embedded resource file:///workspace/context.md (text/markdown):\n# Context") {
		t.Fatalf("embedded resource missing from prompt: %q", joined)
	}
	if _, err := renderPrompt([]promptBlock{{Type: "image"}}); err == nil {
		t.Fatal("expected unsupported image error")
	}
	var blob promptBlock
	blob.Type = "resource"
	blob.Resource.URI = "file:///workspace/data.bin"
	blob.Resource.Blob = "AA=="
	if _, err := renderPrompt([]promptBlock{blob}); err == nil {
		t.Fatal("expected unsupported binary resource error")
	}
}

func TestACPCancelReturnsCancelledStopReason(t *testing.T) {
	root := t.TempDir()
	streamer := &blockingStreamer{started: make(chan struct{})}
	server := &Server{Factory: func(_ context.Context, cfg SessionConfig, approver tools.Approver, text, status io.Writer) (*agent.Runner, func(), error) {
		ws, err := workspace.Open(cfg.CWD)
		if err != nil {
			return nil, nil, err
		}
		registry := tools.NewRegistry(ws, approver)
		return &agent.Runner{Client: streamer, Tools: registry, Model: "fixture", TextOutput: text, StatusOutput: status}, func() { _ = registry.Close() }, nil
	}}
	inputR, inputW := io.Pipe()
	outputR, outputW := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- server.Serve(context.Background(), inputR, outputW) }()
	encoder := json.NewEncoder(inputW)
	decoder := json.NewDecoder(outputR)
	encodeACP(t, encoder, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "session/new", "params": map[string]any{"cwd": root, "mcpServers": []any{}}})
	created := decodeACP(t, decoder)
	sessionID := created["result"].(map[string]any)["sessionId"].(string)
	encodeACP(t, encoder, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "session/prompt", "params": map[string]any{
		"sessionId": sessionID, "prompt": []any{map[string]any{"type": "text", "text": "wait"}},
	}})
	titleUpdate := decodeACP(t, decoder)
	if titleUpdate["method"] != "session/update" {
		t.Fatalf("unexpected title update: %#v", titleUpdate)
	}
	select {
	case <-streamer.started:
	case <-time.After(time.Second):
		t.Fatal("prompt did not start")
	}
	encodeACP(t, encoder, map[string]any{"jsonrpc": "2.0", "method": "session/cancel", "params": map[string]any{"sessionId": sessionID}})
	response := decodeACP(t, decoder)
	if response["result"].(map[string]any)["stopReason"] != "cancelled" {
		t.Fatalf("unexpected cancel response: %#v", response)
	}
	_ = inputW.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ACP server did not stop")
	}
}

func encodeACP(t *testing.T, encoder *json.Encoder, value any) {
	t.Helper()
	if err := encoder.Encode(value); err != nil {
		t.Fatal(err)
	}
}

func decodeACP(t *testing.T, decoder *json.Decoder) map[string]any {
	t.Helper()
	var value map[string]any
	if err := decoder.Decode(&value); err != nil {
		t.Fatal(err)
	}
	return value
}
