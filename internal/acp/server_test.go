package acp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/api"
	sessionlog "github.com/lookcorner/go-cli/internal/session"
	"github.com/lookcorner/go-cli/internal/tools"
	"github.com/lookcorner/go-cli/internal/workspace"
)

type fixtureStreamer struct {
	mu       sync.Mutex
	results  []api.StreamResult
	requests []api.ResponseRequest
}

type blockingStreamer struct{ started chan struct{} }

func TestStartSessionAssignsRunnerSessionID(t *testing.T) {
	root := t.TempDir()
	server := &Server{
		SessionDir: t.TempDir(), sessions: make(map[string]*session),
		Factory: func(_ context.Context, cfg SessionConfig, approver tools.Approver, _, _ io.Writer) (*agent.Runner, func(), error) {
			ws, err := workspace.Open(cfg.CWD)
			if err != nil {
				return nil, nil, err
			}
			registry := tools.NewRegistry(ws, approver)
			return &agent.Runner{Tools: registry}, func() { _ = registry.Close() }, nil
		},
	}
	created, err := server.startSession(context.Background(), "session-123", SessionConfig{CWD: root}, "")
	if err != nil {
		t.Fatal(err)
	}
	closed := false
	defer func() {
		if !closed {
			created.close()
		}
	}()
	if created.runner.SessionID != "session-123" {
		t.Fatalf("runner session ID=%q", created.runner.SessionID)
	}
	created.close()
	closed = true
	statePath := filepath.Join(server.SessionDir, "artifacts", "session-123", "hunks.json")
	if info, err := os.Stat(statePath); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("session hunk state was not persisted: %v", err)
	}
}

func (f *blockingStreamer) StreamResponse(ctx context.Context, _ api.ResponseRequest, _ func(string)) (api.StreamResult, error) {
	close(f.started)
	<-ctx.Done()
	return api.StreamResult{}, ctx.Err()
}

func (f *fixtureStreamer) StreamResponse(ctx context.Context, request api.ResponseRequest, onText func(string)) (api.StreamResult, error) {
	f.mu.Lock()
	f.requests = append(f.requests, request)
	result := f.results[0]
	f.results = f.results[1:]
	f.mu.Unlock()
	if result.Text != "" {
		onText(result.Text)
	}
	return result, nil
}

func TestSessionForkContractAndModelResume(t *testing.T) {
	sessionDir, sourceCWD, newCWD := t.TempDir(), t.TempDir(), t.TempDir()
	logger, err := sessionlog.NewLoggerWithID(sessionDir, "parent")
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []struct {
		kind string
		data any
	}{
		{"session_metadata", map[string]any{"cwd": sourceCWD, "modelId": "old-model"}},
		{"user_prompt", map[string]any{"text": "first"}},
		{"model_response", map[string]any{"text": "one", "response_id": "r1", "tool_call_count": 0}},
		{"user_prompt", map[string]any{"text": "second"}},
		{"model_response", map[string]any{"text": "two", "response_id": "r2", "tool_call_count": 0}},
	} {
		if err := logger.Append(event.kind, event.data); err != nil {
			t.Fatal(err)
		}
	}
	_ = logger.Close()
	configs := make(chan SessionConfig, 1)
	server := &Server{SessionDir: sessionDir, Factory: func(_ context.Context, cfg SessionConfig, _ tools.Approver, _, _ io.Writer) (*agent.Runner, func(), error) {
		configs <- cfg
		return nil, nil, errors.New("stop after config capture")
	}}
	clientToAgentR, clientToAgentW := io.Pipe()
	agentToClientR, agentToClientW := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- server.Serve(context.Background(), clientToAgentR, agentToClientW) }()
	encoder, decoder := json.NewEncoder(clientToAgentW), json.NewDecoder(agentToClientR)
	target := 0
	encodeACP(t, encoder, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "x.ai/session/fork",
		"params": map[string]any{
			"sourceSessionId": "parent", "sourceCwd": sourceCWD, "newCwd": newCWD,
			"newSessionId": "child", "newModelId": "new-model", "targetPromptIndex": target,
		},
	})
	forked := decodeACP(t, decoder)
	result := forked["result"].(map[string]any)
	if result["newSessionId"] != "child" || result["parentSessionId"] != "parent" || result["newModelId"] != "new-model" || result["chatMessagesCopied"].(float64) != 2 {
		t.Fatalf("unexpected fork response: %#v", forked)
	}
	items, err := sessionlog.List(sessionDir, newCWD)
	if err != nil || len(items) != 1 || items[0].ModelID != "new-model" {
		t.Fatalf("fork metadata: %#v err=%v", items, err)
	}
	path, _ := sessionlog.PathForID(sessionDir, "child")
	messages, err := sessionlog.Transcript(path)
	if err != nil || len(messages) != 2 || messages[1].Text != "one" {
		t.Fatalf("fork transcript: %#v err=%v", messages, err)
	}
	encodeACP(t, encoder, map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "session/resume",
		"params": map[string]any{"sessionId": "child", "cwd": newCWD},
	})
	if cfg := <-configs; cfg.Model != "new-model" || cfg.ResumePath != path {
		t.Fatalf("fork model was not resumed: %#v", cfg)
	}
	_ = decodeACP(t, decoder)
	_ = clientToAgentW.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestACPStdioLifecycleStreamingAndPermission(t *testing.T) {
	root := t.TempDir()
	gitInit := exec.Command("git", "init", "-q")
	gitInit.Dir = root
	if output, err := gitInit.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	streamer := &fixtureStreamer{results: []api.StreamResult{
		{ResponseID: "response-1", ToolCalls: []api.ToolCall{{
			CallID: "tool-1", Name: "write_file", Arguments: json.RawMessage(`{"path":"made.txt","content":"ok"}`),
		}}},
		{ResponseID: "response-2", Text: "finished"},
		{ResponseID: "response-3", Text: "replacement answer"},
	}}
	factoryConfigs := make(chan SessionConfig, 1)
	sessionDir := t.TempDir()
	server := &Server{SessionDir: sessionDir, Factory: func(_ context.Context, cfg SessionConfig, approver tools.Approver, text, status io.Writer) (*agent.Runner, func(), error) {
		factoryConfigs <- cfg
		ws, err := workspace.Open(cfg.CWD)
		if err != nil {
			return nil, nil, err
		}
		registry := tools.NewRegistry(ws, approver)
		logger, err := sessionlog.NewLoggerWithID(sessionDir, cfg.SessionID)
		if err != nil {
			_ = registry.Close()
			return nil, nil, err
		}
		if err := logger.Append("session_metadata", map[string]any{"cwd": cfg.CWD}); err != nil {
			_ = logger.Close()
			_ = registry.Close()
			return nil, nil, err
		}
		return &agent.Runner{Client: streamer, Tools: registry, Logger: logger, Model: "fixture", MaxSteps: 3, TextOutput: text, StatusOutput: status}, func() {
			_ = logger.Close()
			_ = registry.Close()
		}, nil
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
	if promptCapabilities["embeddedContext"] != true || promptCapabilities["image"] != true || promptCapabilities["audio"] != false {
		t.Fatalf("unexpected prompt capabilities: %#v", promptCapabilities)
	}
	mcpCapabilities := initialize["result"].(map[string]any)["agentCapabilities"].(map[string]any)["mcpCapabilities"].(map[string]any)
	if mcpCapabilities["http"] != true || mcpCapabilities["sse"] != true {
		t.Fatalf("unexpected MCP capabilities: %#v", mcpCapabilities)
	}
	sessionCapabilities := initialize["result"].(map[string]any)["agentCapabilities"].(map[string]any)["sessionCapabilities"].(map[string]any)
	if _, ok := sessionCapabilities["list"]; !ok || initialize["result"].(map[string]any)["agentCapabilities"].(map[string]any)["loadSession"] != true {
		t.Fatalf("session list capability missing: %#v", sessionCapabilities)
	}
	encodeACP(t, encoder, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "session/new", "params": map[string]any{
		"cwd": root, "mcpServers": []any{map[string]any{
			"name": "client-tools", "command": "/fixture-mcp", "args": []string{"--stdio"},
			"env": []any{map[string]any{"name": "TOKEN", "value": "secret"}},
		}, map[string]any{
			"type": "http", "name": "remote-http", "url": "https://mcp.example/rpc",
			"headers": []any{map[string]any{"name": "Authorization", "value": "Bearer token"}},
		}, map[string]any{
			"type": "sse", "name": "remote-sse", "url": "https://mcp.example/sse",
		}},
	}})
	created := decodeACP(t, decoder)
	receivedConfig := <-factoryConfigs
	if len(receivedConfig.MCPServers) != 3 || receivedConfig.MCPServers[0].Env["TOKEN"] != "secret" ||
		receivedConfig.MCPServers[1].Type != "http" || receivedConfig.MCPServers[1].Headers["Authorization"] != "Bearer token" ||
		receivedConfig.MCPServers[2].Type != "sse" {
		t.Fatalf("client MCP config was not forwarded: %#v", receivedConfig)
	}
	sessionID := created["result"].(map[string]any)["sessionId"].(string)
	modes := created["result"].(map[string]any)["modes"].(map[string]any)
	if modes["currentModeId"] != "default" || len(modes["availableModes"].([]any)) != 3 {
		t.Fatalf("unexpected session modes: %#v", modes)
	}
	encodeACP(t, encoder, map[string]any{"jsonrpc": "2.0", "id": 22, "method": "session/list", "params": map[string]any{"cwd": root}})
	listed := decodeACP(t, decoder)
	sessions := listed["result"].(map[string]any)["sessions"].([]any)
	if len(sessions) != 1 || sessions[0].(map[string]any)["sessionId"] != sessionID {
		t.Fatalf("unexpected session list: %#v", listed)
	}
	encodeACP(t, encoder, map[string]any{"jsonrpc": "2.0", "id": 23, "method": "session/set_mode", "params": map[string]any{
		"sessionId": sessionID, "modeId": "plan",
	}})
	modeUpdate := decodeACP(t, decoder)
	modeData := modeUpdate["params"].(map[string]any)["update"].(map[string]any)
	if modeData["sessionUpdate"] != "current_mode_update" || modeData["currentModeId"] != "plan" {
		t.Fatalf("unexpected mode update: %#v", modeUpdate)
	}
	if response := decodeACP(t, decoder); int(response["id"].(float64)) != 23 {
		t.Fatalf("unexpected set mode response: %#v", response)
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
	encodeACP(t, encoder, map[string]any{
		"jsonrpc": "2.0", "id": 33, "method": "x.ai/hunk-tracker/get-hunks",
		"params": map[string]any{"sessionId": sessionID, "path": "made.txt", "source": "agent"},
	})
	hunkResponse := decodeACP(t, decoder)
	hunkResult := hunkResponse["result"].(map[string]any)
	hunks := hunkResult["hunks"].([]any)
	if len(hunks) != 1 {
		t.Fatalf("unexpected ACP hunks: %#v", hunkResponse)
	}
	hunk := hunks[0].(map[string]any)
	hunkSource := hunk["source"].(map[string]any)
	lineInfo := hunk["lineInfo"].(map[string]any)
	if hunk["path"] != filepath.Join(root, "made.txt") || hunkSource["type"] != "agentEdit" || int(hunkSource["prompt_index"].(float64)) != 0 || int(lineInfo["newStart"].(float64)) != 1 || hunk["patch"] == nil {
		t.Fatalf("unexpected ACP hunks: %#v", hunkResponse)
	}
	if hunkResult["baseline"].(map[string]any)["status"] != "missing" || hunkResult["current"].(map[string]any)["content"] != "ok" || hunkResult["currentContent"] != "ok" {
		t.Fatalf("unexpected ACP file content: %#v", hunkResponse)
	}
	encodeACP(t, encoder, map[string]any{
		"jsonrpc": "2.0", "id": 331, "method": "x.ai/hunk-tracker/get-summary",
		"params": map[string]any{"sessionId": sessionID},
	})
	summaryResult := decodeACP(t, decoder)["result"].(map[string]any)
	summaryStats := summaryResult["stats"].(map[string]any)
	turns := summaryResult["turns"].([]any)
	if len(turns) != 1 {
		t.Fatalf("unexpected ACP hunk summary: %#v", summaryResult)
	}
	turnHunk := turns[0].(map[string]any)["pendingHunks"].([]any)[0].(map[string]any)
	if int(summaryResult["filesModified"].(float64)) != 1 || int(summaryResult["pendingHunks"].(float64)) != 1 || int(summaryStats["acceptedHunks"].(float64)) != 0 || turnHunk["path"] != filepath.Join(root, "made.txt") || turnHunk["patch"] != nil {
		t.Fatalf("unexpected ACP hunk summary: %#v", summaryResult)
	}
	if _, exists := summaryResult["fileCount"]; exists {
		t.Fatalf("ACP hunk summary included non-reference fields: %#v", summaryResult)
	}
	encodeACP(t, encoder, map[string]any{
		"jsonrpc": "2.0", "id": 332, "method": "x.ai/hunk-tracker/get-files",
		"params": map[string]any{"sessionId": sessionID},
	})
	fileItems := decodeACP(t, decoder)["result"].(map[string]any)["files"].([]any)
	if len(fileItems) != 1 || fileItems[0].(map[string]any)["path"] != filepath.Join(root, "made.txt") || fileItems[0].(map[string]any)["isAgentFile"] != true {
		t.Fatalf("unexpected ACP hunk files: %#v", fileItems)
	}
	encodeACP(t, encoder, map[string]any{
		"jsonrpc": "2.0", "id": 333, "method": "x.ai/hunk-tracker/get-hunks",
		"params": map[string]any{"sessionId": sessionID, "source": "future-source"},
	})
	allHunkResult := decodeACP(t, decoder)["result"].(map[string]any)
	allHunks := allHunkResult["hunks"].([]any)
	if len(allHunks) != 1 || allHunks[0].(map[string]any)["patch"] != nil {
		t.Fatalf("unexpected unfiltered ACP hunks: %#v", allHunkResult)
	}
	if _, exists := allHunkResult["baseline"]; exists {
		t.Fatalf("all-hunks response included file content: %#v", allHunkResult)
	}
	encodeACP(t, encoder, map[string]any{
		"jsonrpc": "2.0", "id": 34, "method": "x.ai/hunk-tracker/turn-action",
		"params": map[string]any{"sessionId": sessionID, "promptIndex": 0, "action": "accept"},
	})
	actionResponse := decodeACP(t, decoder)
	actionResult := actionResponse["result"].(map[string]any)
	if actionResult["success"] != true || int(actionResult["affectedCount"].(float64)) != 1 {
		t.Fatalf("unexpected turn action response: %#v", actionResponse)
	}
	encodeACP(t, encoder, map[string]any{
		"jsonrpc": "2.0", "id": 341, "method": "x.ai/hunk-tracker/hunk-action",
		"params": map[string]any{"sessionId": sessionID, "hunkId": hunk["id"], "action": "accept"},
	})
	alreadyAccepted := decodeACP(t, decoder)["result"].(map[string]any)
	if alreadyAccepted["success"] != false || alreadyAccepted["error"] == nil {
		t.Fatalf("accepted hunk action did not fail closed: %#v", alreadyAccepted)
	}
	if _, exists := alreadyAccepted["affectedCount"]; exists {
		t.Fatalf("failed action included affectedCount: %#v", alreadyAccepted)
	}
	encodeACP(t, encoder, map[string]any{
		"jsonrpc": "2.0", "id": 35, "method": "x.ai/hunk-tracker/get-hunks",
		"params": map[string]any{"sessionId": sessionID, "source": "all"},
	})
	acceptedResponse := decodeACP(t, decoder)
	if visible := acceptedResponse["result"].(map[string]any)["hunks"].([]any); len(visible) != 0 {
		t.Fatalf("accepted ACP hunk remained visible: %#v", acceptedResponse)
	}
	encodeACP(t, encoder, map[string]any{
		"jsonrpc": "2.0", "id": 351, "method": "x.ai/hunk-tracker/get-all-file-contents",
		"params": map[string]any{"sessionId": sessionID},
	})
	contentResponse := decodeACP(t, decoder)
	contentFiles := contentResponse["result"].(map[string]any)["files"].([]any)
	if len(contentFiles) != 1 || contentFiles[0].(map[string]any)["path"] != filepath.Join(root, "made.txt") || contentFiles[0].(map[string]any)["current"].(map[string]any)["content"] != "ok" || contentFiles[0].(map[string]any)["isAgentFile"] != true {
		t.Fatalf("unexpected all-file contents: %#v", contentResponse)
	}
	if err := os.WriteFile(filepath.Join(root, "made.txt"), []byte("external"), 0o600); err != nil {
		t.Fatal(err)
	}
	encodeACP(t, encoder, map[string]any{
		"jsonrpc": "2.0", "id": 36, "method": "x.ai/rewind/points",
		"params": map[string]any{"sessionId": sessionID},
	})
	pointsResponse := decodeACP(t, decoder)
	points := pointsResponse["result"].(map[string]any)["rewind_points"].([]any)
	if len(points) != 1 {
		t.Fatalf("unexpected ACP rewind points: %#v", pointsResponse)
	}
	point := points[0].(map[string]any)
	if int(point["prompt_index"].(float64)) != 0 || point["has_file_changes"] != true || int(point["num_file_snapshots"].(float64)) != 1 {
		t.Fatalf("unexpected ACP rewind points: %#v", pointsResponse)
	}
	encodeACP(t, encoder, map[string]any{
		"jsonrpc": "2.0", "id": 37, "method": "x.ai/rewind/execute",
		"params": map[string]any{"sessionId": sessionID, "targetPromptIndex": 0, "force": false, "mode": "all"},
	})
	preview := decodeACP(t, decoder)
	previewResult := preview["result"].(map[string]any)
	if previewResult["success"] != false || len(previewResult["clean_files"].([]any)) != 0 || len(previewResult["conflicts"].([]any)) != 1 || !strings.Contains(previewResult["error"].(string), "External modifications") {
		t.Fatalf("unexpected all-mode rewind preview: %#v", preview)
	}
	encodeACP(t, encoder, map[string]any{
		"jsonrpc": "2.0", "id": 38, "method": "x.ai/rewind/execute",
		"params": map[string]any{"sessionId": sessionID, "targetPromptIndex": 0, "force": true, "mode": "all"},
	})
	rewindUpdate := decodeACP(t, decoder)
	if rewindUpdate["method"] != "session/update" || rewindUpdate["params"].(map[string]any)["update"].(map[string]any)["sessionUpdate"] != "rewind_marker" {
		t.Fatalf("missing ACP rewind marker: %#v", rewindUpdate)
	}
	rewound := decodeACP(t, decoder)
	if rewound["result"].(map[string]any)["success"] != true || rewound["result"].(map[string]any)["prompt_text"] != "create the file" || len(rewound["result"].(map[string]any)["reverted_files"].([]any)) != 1 {
		t.Fatalf("unexpected ACP rewind response: %#v", rewound)
	}
	if _, err := os.Stat(filepath.Join(root, "made.txt")); !os.IsNotExist(err) {
		t.Fatalf("all-mode rewind did not restore files: %v", err)
	}
	encodeACP(t, encoder, map[string]any{
		"jsonrpc": "2.0", "id": 39, "method": "session/prompt",
		"params": map[string]any{"sessionId": sessionID, "prompt": []any{map[string]any{"type": "text", "text": "replacement"}}},
	})
	replacementUpdate := decodeACP(t, decoder)
	if replacementUpdate["method"] != "session/update" {
		t.Fatalf("missing replacement stream update: %#v", replacementUpdate)
	}
	replacementDone := decodeACP(t, decoder)
	if int(replacementDone["id"].(float64)) != 39 {
		t.Fatalf("unexpected replacement completion: %#v", replacementDone)
	}
	streamer.mu.Lock()
	if len(streamer.requests) != 3 || streamer.requests[2].PreviousResponseID != "" || !strings.Contains(streamer.requests[0].Instructions, "Session mode: plan") {
		t.Fatalf("rewound prompt used the discarded response chain: %#v", streamer.requests)
	}
	streamer.mu.Unlock()
	encodeACP(t, encoder, map[string]any{"jsonrpc": "2.0", "id": 4, "method": "session/close", "params": map[string]any{"sessionId": sessionID}})
	closed := decodeACP(t, decoder)
	if int(closed["id"].(float64)) != 4 {
		t.Fatalf("unexpected close response: %#v", closed)
	}
	path, err := sessionlog.PathForID(sessionDir, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if mode, err := sessionlog.CurrentMode(path); err != nil || mode != "plan" {
		t.Fatalf("persisted mode=%q err=%v", mode, err)
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

func TestParseMCPServersRejectsInvalidWireValues(t *testing.T) {
	tests := []string{
		`[{"name":"missing-command"}]`,
		`[{"type":"http","name":"bad-url","url":"file:///tmp/socket"}]`,
		`[{"type":"sse","name":"bad-header","url":"https://example.com/sse","headers":[{"name":"Bad Header","value":"x"}]}]`,
		`[{"type":"http","name":"bad-value","url":"https://example.com/mcp","headers":[{"name":"X-Test","value":"x\r\ny"}]}]`,
		`[{"type":"websocket","name":"unknown"}]`,
	}
	for _, raw := range tests {
		var params []mcpServerParam
		if err := json.Unmarshal([]byte(raw), &params); err != nil {
			t.Fatal(err)
		}
		if _, err := parseMCPServers(params); err == nil {
			t.Errorf("invalid MCP servers were accepted: %s", raw)
		}
	}
}

func TestRenderPromptSupportsEmbeddedTextAndImages(t *testing.T) {
	var embedded promptBlock
	embedded.Type = "resource"
	embedded.Resource.URI = "file:///workspace/context.md"
	embedded.Resource.MimeType = "text/markdown"
	embedded.Resource.Text = "# Context"
	prompt, content, err := renderPrompt([]promptBlock{
		{Type: "text", Text: "Use this context"},
		embedded,
		{Type: "resource_link", Name: "spec", URI: "file:///workspace/spec.md"},
		{Type: "image", MimeType: "image/png", Data: "aGVsbG8="},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, "Embedded resource file:///workspace/context.md (text/markdown):\n# Context") {
		t.Fatalf("embedded resource missing from prompt: %q", prompt)
	}
	if len(content) != 4 || content[3].Type != "input_image" || content[3].ImageURL != "data:image/png;base64,aGVsbG8=" {
		t.Fatalf("image missing from prompt content: %#v", content)
	}
	_, remote, err := renderPrompt([]promptBlock{{Type: "image", URI: "https://example.com/image.png"}})
	if err != nil || len(remote) != 1 || remote[0].ImageURL != "https://example.com/image.png" {
		t.Fatalf("remote image was not preserved: content=%#v err=%v", remote, err)
	}
	for _, block := range []promptBlock{
		{Type: "image"},
		{Type: "image", MimeType: "image/svg+xml", Data: "PHN2Zz4="},
		{Type: "image", MimeType: "image/png", Data: "not-base64"},
		{Type: "audio"},
	} {
		if _, _, err := renderPrompt([]promptBlock{block}); err == nil {
			t.Errorf("expected unsupported media error for %#v", block)
		}
	}
	var blob promptBlock
	blob.Type = "resource"
	blob.Resource.URI = "file:///workspace/data.bin"
	blob.Resource.Blob = "AA=="
	if _, _, err := renderPrompt([]promptBlock{blob}); err == nil {
		t.Fatal("expected unsupported binary resource error")
	}
}

func TestACPToolUpdateIncludesImageContent(t *testing.T) {
	var output strings.Builder
	server := &Server{output: &output}
	observer := &sessionToolObserver{server: server, sessionID: "session-1"}
	observer.ToolFinished(api.ToolCall{CallID: "call-1"}, tools.ExecutionResult{
		Output: "[PDF: doc.pdf (2 pages rendered, 2 total)]",
		Images: []tools.ImageAttachment{
			{MediaType: "image/jpeg", Data: []byte("page-one")},
			{MediaType: "image/jpeg", Data: []byte("page-two")},
		},
	}, nil)
	var notification map[string]any
	if err := json.Unmarshal([]byte(output.String()), &notification); err != nil {
		t.Fatal(err)
	}
	update := notification["params"].(map[string]any)["update"].(map[string]any)
	content := update["content"].([]any)
	if len(content) != 2 || update["status"] != "completed" {
		t.Fatalf("unexpected tool update: %#v", update)
	}
	first := content[0].(map[string]any)
	image := first["content"].(map[string]any)
	if first["type"] != "content" || image["type"] != "image" || image["mimeType"] != "image/jpeg" || image["data"] != base64.StdEncoding.EncodeToString([]byte("page-one")) {
		t.Fatalf("unexpected image content: %#v", first)
	}
}

func TestWorktreeExtensionsCreateListShowAndRemove(t *testing.T) {
	root := t.TempDir()
	runACPGit(t, root, "init", "-q")
	runACPGit(t, root, "config", "user.name", "Fixture")
	runACPGit(t, root, "config", "user.email", "fixture@example.invalid")
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("ignored/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runACPGit(t, root, "add", "tracked.txt", ".gitignore")
	runACPGit(t, root, "commit", "-qm", "baseline")
	if err := os.MkdirAll(filepath.Join(root, "ignored"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "ignored", "keep.txt"), []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "ignored", "skip.log"), []byte("skip\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "worktree")

	clientToAgentR, clientToAgentW := io.Pipe()
	agentToClientR, agentToClientW := io.Pipe()
	server := &Server{
		SessionDir: t.TempDir(),
		Factory: func(context.Context, SessionConfig, tools.Approver, io.Writer, io.Writer) (*agent.Runner, func(), error) {
			return nil, nil, errors.New("session factory should not be called")
		},
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(context.Background(), clientToAgentR, agentToClientW) }()
	encoder := json.NewEncoder(clientToAgentW)
	decoder := json.NewDecoder(agentToClientR)
	encodeACP(t, encoder, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "x.ai/git/worktree/create",
		"params": map[string]any{
			"sessionId": "wt-session", "sourcePath": root, "worktreePath": dest,
			"copyMode": "clean", "worktreeType": "linked", "label": "ACP Test",
			"copyIgnoredInBackground": true, "ignoredSkipPatterns": []string{"ignored/skip.log"},
		},
	})
	created := decodeACP(t, decoder)
	createdResult := created["result"].(map[string]any)
	if createdResult["status"] != "creating" || createdResult["worktreePath"] != dest {
		t.Fatalf("unexpected create response: %#v", created)
	}
	notification := decodeACP(t, decoder)
	if notification["method"] != "x.ai/git/worktree/status" || notification["params"].(map[string]any)["status"] != "created" {
		t.Fatalf("unexpected worktree notification: %#v", notification)
	}
	copyingIgnored := decodeACP(t, decoder)
	if copyingIgnored["params"].(map[string]any)["status"] != "copyingIgnored" {
		t.Fatalf("unexpected ignored-copy start: %#v", copyingIgnored)
	}
	ignoredComplete := decodeACP(t, decoder)
	completeParams := ignoredComplete["params"].(map[string]any)
	if completeParams["status"] != "ignoredCopyComplete" || int(completeParams["filesCopied"].(float64)) != 1 {
		t.Fatalf("unexpected ignored-copy completion: %#v", ignoredComplete)
	}
	if data, err := os.ReadFile(filepath.Join(dest, "ignored", "keep.txt")); err != nil || string(data) != "keep\n" {
		t.Fatalf("ignored file was not copied: %q err=%v", data, err)
	}
	if _, err := os.Stat(filepath.Join(dest, "ignored", "skip.log")); !os.IsNotExist(err) {
		t.Fatalf("skipped ignored file was copied: %v", err)
	}
	encodeACP(t, encoder, map[string]any{
		"jsonrpc": "2.0", "id": 11, "method": "x.ai/git/worktree/create",
		"params": map[string]any{
			"sessionId": "wt-session", "sourcePath": root, "worktreePath": dest,
			"copyMode": "clean", "worktreeType": "linked", "label": "ACP Test",
		},
	})
	existing := decodeACP(t, decoder)["result"].(map[string]any)
	if existing["status"] != "exists" || existing["commit"] == nil {
		t.Fatalf("unexpected existing worktree response: %#v", existing)
	}
	encodeACP(t, encoder, map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "x.ai/git/worktree/list", "params": map[string]any{},
	})
	listed := decodeACP(t, decoder)
	records := listed["result"].([]any)
	if len(records) != 1 {
		t.Fatalf("unexpected worktree list: %#v", listed)
	}
	listedRecord := records[0].(map[string]any)
	if listedRecord["path"] != dest || listedRecord["session_id"] != "wt-session" || listedRecord["source_repo"] == nil || listedRecord["created_at"] == nil {
		t.Fatalf("unexpected worktree list: %#v", listed)
	}
	if _, exists := listedRecord["sessionId"]; exists {
		t.Fatalf("worktree list used non-reference field names: %#v", listedRecord)
	}
	encodeACP(t, encoder, map[string]any{
		"jsonrpc": "2.0", "id": 3, "method": "x.ai/git/worktree/show", "params": map[string]any{"idOrPath": dest},
	})
	shown := decodeACP(t, decoder)
	if shown["result"].(map[string]any)["session_id"] != "wt-session" {
		t.Fatalf("unexpected worktree show: %#v", shown)
	}
	if err := os.WriteFile(filepath.Join(dest, "tracked.txt"), []byte("applied\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	encodeACP(t, encoder, map[string]any{
		"jsonrpc": "2.0", "id": 4, "method": "x.ai/git/worktree/apply",
		"params": map[string]any{"sessionId": "wt-session", "worktreePath": dest, "mode": "merge"},
	})
	applied := decodeACP(t, decoder)
	if applied["result"].(map[string]any)["status"] != "success" {
		t.Fatalf("unexpected worktree apply: %#v", applied)
	}
	if data, err := os.ReadFile(filepath.Join(root, "tracked.txt")); err != nil || string(data) != "applied\n" {
		t.Fatalf("ACP apply did not update source: %q err=%v", data, err)
	}
	if err := os.WriteFile(filepath.Join(dest, "fork-only.txt"), []byte("forked\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	encodeACP(t, encoder, map[string]any{
		"jsonrpc": "2.0", "id": 40, "method": "x.ai/git/worktree/create_from_worktree_sync",
		"params": map[string]any{
			"sourceWorktreePath": dest, "newSessionId": "fork-session", "copyMode": "dirty",
			"worktreeType": "linked", "label": "fork-child",
		},
	})
	forked := decodeACP(t, decoder)
	forkResult := forked["result"].(map[string]any)
	if forkResult["status"] != "created" || forkResult["newSessionId"] != "fork-session" {
		t.Fatalf("unexpected worktree fork: %#v", forked)
	}
	forkPath := forkResult["worktreePath"].(string)
	if data, err := os.ReadFile(filepath.Join(forkPath, "fork-only.txt")); err != nil || string(data) != "forked\n" {
		t.Fatalf("fork did not copy dirty state: %q err=%v", data, err)
	}
	encodeACP(t, encoder, map[string]any{
		"jsonrpc": "2.0", "id": 41, "method": "x.ai/git/worktree/remove",
		"params": map[string]any{"worktreePath": forkPath, "force": true},
	})
	if forkRemoved := decodeACP(t, decoder); forkRemoved["result"].(map[string]any)["removed"] != true {
		t.Fatalf("fork removal failed: %#v", forkRemoved)
	}
	encodeACP(t, encoder, map[string]any{"jsonrpc": "2.0", "id": 42, "method": "x.ai/git/worktree/db/stats", "params": map[string]any{}})
	stats := decodeACP(t, decoder)
	if stats["result"].(map[string]any)["total_records"].(float64) != 1 {
		t.Fatalf("unexpected worktree stats: %#v", stats)
	}
	encodeACP(t, encoder, map[string]any{"jsonrpc": "2.0", "id": 43, "method": "x.ai/git/worktree/db/path", "params": map[string]any{}})
	dbPath := decodeACP(t, decoder)
	if !strings.HasSuffix(dbPath["result"].(map[string]any)["path"].(string), "worktrees.json") {
		t.Fatalf("unexpected worktree DB path: %#v", dbPath)
	}
	encodeACP(t, encoder, map[string]any{"jsonrpc": "2.0", "id": 45, "method": "x.ai/git/worktree/db/rebuild", "params": map[string]any{}})
	rebuild := decodeACP(t, decoder)["result"].(map[string]any)
	if _, exists := rebuild["already_tracked"]; !exists {
		t.Fatalf("unexpected worktree rebuild report: %#v", rebuild)
	}
	encodeACP(t, encoder, map[string]any{
		"jsonrpc": "2.0", "id": 44, "method": "x.ai/git/worktree/gc",
		"params": map[string]any{"dryRun": true, "maxAge": "0s", "force": true},
	})
	gc := decodeACP(t, decoder)
	if gc["result"].(map[string]any)["expired_removed"].(float64) != 1 {
		t.Fatalf("unexpected worktree GC dry-run: %#v", gc)
	}
	if _, err := os.Stat(dest); err != nil {
		t.Fatalf("GC dry-run removed worktree: %v", err)
	}
	encodeACP(t, encoder, map[string]any{
		"jsonrpc": "2.0", "id": 5, "method": "x.ai/git/worktree/remove",
		"params": map[string]any{"worktreePath": dest, "dryRun": true},
	})
	dryRun := decodeACP(t, decoder)
	if dryRun["result"].(map[string]any)["removed"] != false {
		t.Fatalf("unexpected worktree dry-run: %#v", dryRun)
	}
	encodeACP(t, encoder, map[string]any{
		"jsonrpc": "2.0", "id": 6, "method": "x.ai/git/worktree/remove",
		"params": map[string]any{"worktreePath": dest, "force": true},
	})
	removed := decodeACP(t, decoder)
	if removed["result"].(map[string]any)["removed"] != true {
		t.Fatalf("unexpected worktree remove: %#v", removed)
	}
	_ = clientToAgentW.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func runACPGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	_ = runACPGitOutput(t, dir, args...)
}

func runACPGitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return string(output)
}

func TestSessionWorktreeResumeAndRehydrate(t *testing.T) {
	root := t.TempDir()
	runACPGit(t, root, "init", "-q")
	runACPGit(t, root, "config", "user.name", "Fixture")
	runACPGit(t, root, "config", "user.email", "fixture@example.invalid")
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runACPGit(t, root, "add", "tracked.txt")
	runACPGit(t, root, "commit", "-qm", "baseline")
	historicalHead := strings.TrimSpace(runACPGitOutput(t, root, "rev-parse", "HEAD"))
	sessionDir := t.TempDir()
	logger, err := sessionlog.NewLoggerWithID(sessionDir, "resume-parent")
	if err != nil {
		t.Fatal(err)
	}
	_ = logger.Append("session_metadata", map[string]any{"cwd": root, "headCommit": historicalHead})
	_ = logger.Append("user_prompt", map[string]any{"text": "resume me"})
	_ = logger.Append("model_response", map[string]any{"text": "ready", "response_id": "r1", "tool_call_count": 0})
	_ = logger.Close()
	if err := os.WriteFile(filepath.Join(root, "later.txt"), []byte("later\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runACPGit(t, root, "add", "later.txt")
	runACPGit(t, root, "commit", "-qm", "later")

	clientToAgentR, clientToAgentW := io.Pipe()
	agentToClientR, agentToClientW := io.Pipe()
	server := &Server{SessionDir: sessionDir, Factory: func(context.Context, SessionConfig, tools.Approver, io.Writer, io.Writer) (*agent.Runner, func(), error) {
		return nil, nil, errors.New("session factory should not be called")
	}}
	done := make(chan error, 1)
	go func() { done <- server.Serve(context.Background(), clientToAgentR, agentToClientW) }()
	encoder, decoder := json.NewEncoder(clientToAgentW), json.NewDecoder(agentToClientR)
	encodeACP(t, encoder, map[string]any{
		"jsonrpc": "2.0", "id": 0, "method": "x.ai/session/resolve_local_for_worktree_resume",
		"params": map[string]any{"sessionId": "resume-parent", "cwd": root},
	})
	resolved := decodeACP(t, decoder)
	if result := resolved["result"].(map[string]any); result["found"] != true || result["resolutionKind"] != "exactCwd" {
		t.Fatalf("unexpected local resolution: %#v", resolved)
	}
	encodeACP(t, encoder, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "x.ai/git/worktree/resume_session",
		"params": map[string]any{"sessionId": "resume-parent", "sourceCwd": root, "copyMode": "clean", "worktreeType": "linked", "restoreCode": true},
	})
	resumed := decodeACP(t, decoder)
	result := resumed["result"].(map[string]any)
	if result["parentSessionId"] != "resume-parent" || result["remoteRestored"] != false || result["chatMessagesCopied"].(float64) != 2 {
		t.Fatalf("unexpected resume response: %#v", resumed)
	}
	resumedID, resumedPath := result["sessionId"].(string), result["worktreePath"].(string)
	if result["codeRestored"] != true || result["restoreDegree"] != "head_only" || strings.TrimSpace(runACPGitOutput(t, resumedPath, "rev-parse", "HEAD")) != historicalHead {
		t.Fatalf("historical HEAD was not restored: %#v", resumed)
	}
	if items, err := sessionlog.List(sessionDir, result["effectiveCwd"].(string)); err != nil || len(items) != 1 || items[0].SessionID != resumedID {
		t.Fatalf("forked session not loadable: %#v err=%v", items, err)
	}
	encodeACP(t, encoder, map[string]any{
		"jsonrpc": "2.0", "id": 10, "method": "x.ai/session/resolve_local_for_worktree_resume",
		"params": map[string]any{"sessionId": "resume-parent", "cwd": resumedPath},
	})
	resolvedSibling := decodeACP(t, decoder)
	if result := resolvedSibling["result"].(map[string]any); result["found"] != true || result["resolutionKind"] != "sameRepoDifferentCwd" {
		t.Fatalf("unexpected sibling resolution: %#v", resolvedSibling)
	}
	rehydratedPath := filepath.Join(t.TempDir(), "rehydrated")
	encodeACP(t, encoder, map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "x.ai/session/rehydrate",
		"params": map[string]any{"sessionId": "resume-parent", "sourceCwd": rehydratedPath, "repoRoot": root, "worktreePath": rehydratedPath},
	})
	rehydrated := decodeACP(t, decoder)
	if rehydrated["result"].(map[string]any)["codebaseRestored"] != true {
		t.Fatalf("unexpected rehydrate response: %#v", rehydrated)
	}
	if _, err := os.Stat(filepath.Join(rehydratedPath, "tracked.txt")); err != nil {
		t.Fatalf("rehydrated worktree missing: %v", err)
	}
	for id, path := range map[int]string{3: resumedPath, 4: rehydratedPath} {
		encodeACP(t, encoder, map[string]any{"jsonrpc": "2.0", "id": id, "method": "x.ai/git/worktree/remove", "params": map[string]any{"worktreePath": path, "force": true}})
		if response := decodeACP(t, decoder); response["result"].(map[string]any)["removed"] != true {
			t.Fatalf("cleanup failed: %#v", response)
		}
	}
	_ = clientToAgentW.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestACPCancelReturnsCancelledStopReason(t *testing.T) {
	root := t.TempDir()
	streamer := &blockingStreamer{started: make(chan struct{})}
	server := &Server{SessionDir: t.TempDir(), Factory: func(_ context.Context, cfg SessionConfig, approver tools.Approver, text, status io.Writer) (*agent.Runner, func(), error) {
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

func TestACPLoadReplaysAndResumeReconnectsPersistedSession(t *testing.T) {
	sessionDir := t.TempDir()
	workspaceRoot := t.TempDir()
	logger, err := sessionlog.NewLoggerWithID(sessionDir, "persisted-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := logger.Append("session_metadata", map[string]any{"cwd": workspaceRoot}); err != nil {
		t.Fatal(err)
	}
	imageData := base64.StdEncoding.EncodeToString([]byte{137, 80, 78, 71, 13, 10, 26, 10})
	if err := logger.AppendPrompt("stored question", []sessionlog.Content{
		{Type: "text", Text: "stored question"},
		{Type: "image", URI: "data:image/png;base64," + imageData},
	}); err != nil {
		t.Fatal(err)
	}
	if err := logger.Append("model_response", map[string]any{"response_id": "stored-response", "text": "stored answer", "tool_call_count": 0}); err != nil {
		t.Fatal(err)
	}
	if err := logger.Append("session_mode", map[string]any{"mode_id": "plan"}); err != nil {
		t.Fatal(err)
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	server := &Server{SessionDir: sessionDir, Factory: func(_ context.Context, cfg SessionConfig, approver tools.Approver, text, status io.Writer) (*agent.Runner, func(), error) {
		ws, err := workspace.Open(cfg.CWD)
		if err != nil {
			return nil, nil, err
		}
		registry := tools.NewRegistry(ws, approver)
		resumed, _, err := sessionlog.Resume(cfg.ResumePath)
		if err != nil {
			_ = registry.Close()
			return nil, nil, err
		}
		return &agent.Runner{
			Client: &fixtureStreamer{}, Tools: registry, Logger: resumed,
			Model: "fixture", TextOutput: text, StatusOutput: status,
		}, func() { _ = resumed.Close(); _ = registry.Close() }, nil
	}}
	inputR, inputW := io.Pipe()
	outputR, outputW := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- server.Serve(context.Background(), inputR, outputW) }()
	encoder := json.NewEncoder(inputW)
	decoder := json.NewDecoder(outputR)
	loadParams := map[string]any{"sessionId": "persisted-1", "cwd": workspaceRoot, "mcpServers": []any{}}
	encodeACP(t, encoder, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "session/load", "params": loadParams})
	userTextReplay := decodeACP(t, decoder)
	userImageReplay := decodeACP(t, decoder)
	agentReplay := decodeACP(t, decoder)
	loaded := decodeACP(t, decoder)
	if userTextReplay["params"].(map[string]any)["update"].(map[string]any)["sessionUpdate"] != "user_message_chunk" ||
		userImageReplay["params"].(map[string]any)["update"].(map[string]any)["content"].(map[string]any)["data"] != imageData ||
		agentReplay["params"].(map[string]any)["update"].(map[string]any)["sessionUpdate"] != "agent_message_chunk" ||
		loaded["result"].(map[string]any)["sessionId"] != "persisted-1" {
		t.Fatalf("unexpected load sequence: %#v %#v %#v %#v", userTextReplay, userImageReplay, agentReplay, loaded)
	}
	if loaded["result"].(map[string]any)["modes"].(map[string]any)["currentModeId"] != "plan" {
		t.Fatalf("loaded mode was not restored: %#v", loaded)
	}
	encodeACP(t, encoder, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "session/close", "params": map[string]any{"sessionId": "persisted-1"}})
	_ = decodeACP(t, decoder)
	encodeACP(t, encoder, map[string]any{"jsonrpc": "2.0", "id": 3, "method": "session/resume", "params": loadParams})
	resumed := decodeACP(t, decoder)
	if int(resumed["id"].(float64)) != 3 || resumed["result"].(map[string]any)["sessionId"] != "persisted-1" {
		t.Fatalf("unexpected resume response: %#v", resumed)
	}
	if resumed["result"].(map[string]any)["modes"].(map[string]any)["currentModeId"] != "plan" {
		t.Fatalf("resumed mode was not restored: %#v", resumed)
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
