package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"testing"

	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/api"
	"github.com/lookcorner/go-cli/internal/tools"
	"github.com/lookcorner/go-cli/internal/workspace"
)

func TestDebugArmAutoCompactDrivesNextTurn(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry(ws, tools.PromptApprover{Mode: tools.PermissionAuto})
	defer registry.Close()
	streamer := &fixtureStreamer{results: []api.StreamResult{
		{ResponseID: "old", Usage: api.Usage{InputTokens: 100}},
		{Text: "summary"},
		{ResponseID: "fresh", Usage: api.Usage{InputTokens: 100}},
	}}
	runner := &agent.Runner{Client: streamer, Tools: registry, Model: "test", ContextWindow: 1000, CompactThresholdPercent: 85}
	if _, err := runner.RunTurn(context.Background(), "first", ""); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	server := &Server{output: &output, sessions: map[string]*session{"debug": {id: "debug", runner: runner}}}
	server.handleDebug(message{ID: json.RawMessage("1"), Method: "x.ai/debug/arm_auto_compact", Params: json.RawMessage(`{"session_id":"debug"}`)})
	response := decodeACPOutput(t, output.Bytes())
	if len(response) != 1 || response[0]["result"].(map[string]any)["result"].(map[string]any)["armed"] != true {
		t.Fatalf("response=%#v", response)
	}
	if result, err := runner.RunTurn(context.Background(), "continue", "old"); err != nil || result.ResponseID != "fresh" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	streamer.mu.Lock()
	requests := append([]api.ResponseRequest(nil), streamer.requests...)
	streamer.mu.Unlock()
	if len(requests) != 3 || requests[1].PreviousResponseID != "old" || requests[2].PreviousResponseID != "" {
		t.Fatalf("requests=%#v", requests)
	}
}

func TestDebugArmAutoCompactValidationAndRoute(t *testing.T) {
	var output bytes.Buffer
	server := &Server{output: &output, sessions: map[string]*session{
		"closed": {id: "closed", closed: true},
	}}
	server.handleDebug(message{ID: json.RawMessage("1"), Params: json.RawMessage(`{`)})
	server.handleDebug(message{ID: json.RawMessage("2"), Params: json.RawMessage(`{}`)})
	server.handleDebug(message{ID: json.RawMessage("3"), Params: json.RawMessage(`{"sessionId":"missing"}`)})
	server.handleDebug(message{ID: json.RawMessage("4"), Params: json.RawMessage(`{"sessionId":"closed"}`)})
	messages := decodeACPOutput(t, output.Bytes())
	if len(messages) != 4 ||
		messages[0]["error"].(map[string]any)["message"] != "invalid debug parameters" ||
		messages[1]["error"].(map[string]any)["message"] != "sessionId required" ||
		messages[2]["error"].(map[string]any)["message"] != "unknown session id" ||
		messages[3]["error"].(map[string]any)["message"] != "unknown session id" {
		t.Fatalf("messages=%#v", messages)
	}

	output.Reset()
	routed := &Server{Factory: func(context.Context, SessionConfig, tools.Approver, io.Writer, io.Writer) (*agent.Runner, func(), error) {
		return nil, nil, nil
	}}
	input := bytes.NewBufferString(`{"jsonrpc":"2.0","id":3,"method":"x.ai/debug/arm_auto_compact","params":{"sessionId":"missing"}}`)
	if err := routed.Serve(context.Background(), input, &output); err != nil {
		t.Fatal(err)
	}
	messages = decodeACPOutput(t, output.Bytes())
	if len(messages) != 1 || messages[0]["id"] != float64(3) || messages[0]["error"].(map[string]any)["message"] != "unknown session id" {
		t.Fatalf("routed messages=%#v", messages)
	}
}
