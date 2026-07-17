package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lookcorner/go-cli/internal/api"
	"github.com/lookcorner/go-cli/internal/tools"
	"github.com/lookcorner/go-cli/internal/workspace"
)

type fakeStreamer struct {
	requests []api.ResponseRequest
	results  []api.StreamResult
}

func (f *fakeStreamer) StreamResponse(_ context.Context, request api.ResponseRequest, onText func(string)) (api.StreamResult, error) {
	f.requests = append(f.requests, request)
	result := f.results[len(f.requests)-1]
	if onText != nil && result.Text != "" {
		onText(result.Text)
	}
	return result, nil
}

func TestRunnerExecutesToolLoop(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("hello agent\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	streamer := &fakeStreamer{results: []api.StreamResult{
		{ResponseID: "resp_1", ToolCalls: []api.ToolCall{{CallID: "call_1", Name: "read_file", Arguments: json.RawMessage(`{"path":"README.md"}`)}}},
		{ResponseID: "resp_2", Text: "done"},
	}}
	var output bytes.Buffer
	runner := Runner{
		Client: streamer,
		Tools:  tools.NewRegistry(ws, tools.PromptApprover{Mode: tools.PermissionDeny}),
		Model:  "test-model", MaxSteps: 3, TextOutput: &output,
	}
	result, err := runner.RunTurn(context.Background(), "inspect the readme", "resp_0")
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "done" || result.Steps != 2 || output.String() != "done" {
		t.Fatalf("unexpected result=%#v output=%q", result, output.String())
	}
	if len(streamer.requests) != 2 {
		t.Fatalf("expected two requests, got %d", len(streamer.requests))
	}
	if streamer.requests[0].PreviousResponseID != "resp_0" {
		t.Fatalf("first request did not continue prior conversation: %#v", streamer.requests[0])
	}
	second := streamer.requests[1]
	if second.PreviousResponseID != "resp_1" || len(second.Input) != 1 {
		t.Fatalf("unexpected continuation: %#v", second)
	}
	if second.Input[0].Type != "function_call_output" || second.Input[0].CallID != "call_1" {
		t.Fatalf("unexpected tool output item: %#v", second.Input[0])
	}
}

func TestRunnerCompactsAtUsageThresholdAndStartsFreshChain(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	streamer := &fakeStreamer{results: []api.StreamResult{
		{ResponseID: "old-response", Text: "first answer", Usage: api.Usage{InputTokens: 900}},
		{ResponseID: "summary-response", Text: "Preserve the implementation state."},
		{ResponseID: "fresh-response", Text: "continued", Usage: api.Usage{InputTokens: 100}},
	}}
	runner := Runner{
		Client: streamer, Tools: tools.NewRegistry(ws, tools.PromptApprover{Mode: tools.PermissionAuto}),
		Model: "test-model", ContextWindow: 1000, CompactThresholdPercent: 85,
	}
	defer runner.Tools.Close()
	if _, err := runner.RunTurn(context.Background(), "first", ""); err != nil {
		t.Fatal(err)
	}
	result, err := runner.RunTurn(context.Background(), "continue", "old-response")
	if err != nil {
		t.Fatal(err)
	}
	if result.ResponseID != "fresh-response" || len(streamer.requests) != 3 {
		t.Fatalf("unexpected compacted result: %#v requests=%d", result, len(streamer.requests))
	}
	if streamer.requests[1].PreviousResponseID != "old-response" || len(streamer.requests[1].Tools) != 0 {
		t.Fatalf("invalid compaction request: %#v", streamer.requests[1])
	}
	if streamer.requests[2].PreviousResponseID != "" || !strings.Contains(streamer.requests[2].Input[0].Content.(string), "Preserve the implementation state") {
		t.Fatalf("fresh chain did not receive summary: %#v", streamer.requests[2])
	}
}
