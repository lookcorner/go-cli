package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/api"
	"github.com/lookcorner/go-cli/internal/tools"
	"github.com/lookcorner/go-cli/internal/workspace"
)

func TestInterjectQueuesActiveTurnAndBroadcasts(t *testing.T) {
	var output bytes.Buffer
	runner := &agent.Runner{}
	current := &session{id: "active", runner: runner, running: true, activePrompt: 0}
	server := &Server{output: &output, sessions: map[string]*session{"active": current}}
	server.handleInterject(message{
		ID: json.RawMessage("1"), Method: "x.ai/interject",
		Params: json.RawMessage(`{"sessionId":"active","text":"raw path","interjectionId":"ij-1","content":[{"type":"text","text":"steer now"},{"type":"image","data":"iVBORw0KGgo=","mimeType":"image/png"}]}`),
	})

	pending := runner.TakeInterjections()
	if len(pending) != 1 || pending[0].Text != "steer now" || len(pending[0].Content) != 1 || pending[0].Content[0].ImageURL != "data:image/png;base64,iVBORw0KGgo=" {
		t.Fatalf("pending=%#v", pending)
	}
	decoder := json.NewDecoder(&output)
	var notification, response map[string]any
	if err := decoder.Decode(&notification); err != nil {
		t.Fatal(err)
	}
	if err := decoder.Decode(&response); err != nil {
		t.Fatal(err)
	}
	params := notification["params"].(map[string]any)
	if notification["method"] != "x.ai/session/interjection" || params["sessionId"] != "active" || params["text"] != "steer now" || params["interjectionId"] != "ij-1" {
		t.Fatalf("notification=%#v", notification)
	}
	if response["result"].(map[string]any)["result"].(map[string]any)["status"] != "queued" {
		t.Fatalf("response=%#v", response)
	}
}

func TestInterjectIdleSessionStartsFallbackTurn(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry(ws, tools.PromptApprover{Mode: tools.PermissionAuto})
	defer registry.Close()
	streamer := &fixtureStreamer{results: []api.StreamResult{{ResponseID: "fallback-response", Text: "done"}}}
	runner := &agent.Runner{Client: streamer, Tools: registry, Model: "test", MaxSteps: 1}
	current := &session{id: "idle", ctx: context.Background(), runner: runner, activePrompt: -1}
	var output bytes.Buffer
	server := &Server{output: &output, sessions: map[string]*session{"idle": current}}
	server.handleInterject(message{ID: json.RawMessage("1"), Params: json.RawMessage(`{"sessionId":"idle","text":"run next"}`)})
	server.wg.Wait()

	streamer.mu.Lock()
	requests := append([]api.ResponseRequest(nil), streamer.requests...)
	streamer.mu.Unlock()
	if len(requests) != 1 || requests[0].PreviousResponseID != "" {
		t.Fatalf("requests=%#v", requests)
	}
	parts := requests[0].Input[0].Content.([]api.ContentPart)
	if len(parts) != 1 || parts[0].Text != "run next" {
		t.Fatalf("fallback content=%#v", requests[0].Input)
	}
	current.mu.Lock()
	defer current.mu.Unlock()
	if current.running || current.previous != "fallback-response" || len(current.interjectionQueue) != 0 {
		t.Fatalf("session state=%#v", current)
	}
}

func TestInterjectionFallbackRunsBeforeBackgroundWake(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry(ws, tools.PromptApprover{Mode: tools.PermissionAuto})
	defer registry.Close()
	streamer := &fixtureStreamer{results: []api.StreamResult{
		{ResponseID: "interjection-response", Text: "steered"},
		{ResponseID: "wake-response", Text: "woke"},
	}}
	current := &session{
		id: "ordered", ctx: context.Background(), runner: &agent.Runner{Client: streamer, Tools: registry, Model: "test", MaxSteps: 1}, activePrompt: -1,
		interjectionQueue: []agent.Interjection{{Text: "steer first"}},
		wakeQueue:         []syntheticWake{{prompt: "background later"}},
	}
	server := &Server{}
	server.startNextWake(current)
	server.wg.Wait()

	streamer.mu.Lock()
	requests := append([]api.ResponseRequest(nil), streamer.requests...)
	streamer.mu.Unlock()
	if len(requests) != 2 || requests[1].PreviousResponseID != "interjection-response" {
		t.Fatalf("requests=%#v", requests)
	}
	first, _ := json.Marshal(requests[0].Input)
	second, _ := json.Marshal(requests[1].Input)
	if !strings.Contains(string(first), "steer first") || !strings.Contains(string(second), "background later") {
		t.Fatalf("fallback order first=%s second=%s", first, second)
	}
}

func TestCancelClearsPendingInterjections(t *testing.T) {
	runner := &agent.Runner{}
	runner.QueueInterjection("active", nil)
	current := &session{runner: runner, interjectionQueue: []agent.Interjection{{Text: "fallback"}}}
	server := &Server{sessions: map[string]*session{"cancel": current}}
	server.handleCancel(json.RawMessage(`{"sessionId":"cancel"}`))
	if pending := runner.TakeInterjections(); len(pending) != 0 || len(current.interjectionQueue) != 0 {
		t.Fatalf("runner=%#v fallback=%#v", pending, current.interjectionQueue)
	}
}

func TestInterjectRejectsInvalidRequests(t *testing.T) {
	tests := []struct {
		name   string
		params string
		want   string
	}{
		{name: "missing text", params: `{"sessionId":"active"}`, want: "sessionId and text are required"},
		{name: "unknown session", params: `{"sessionId":"missing","text":"steer"}`, want: "session not found"},
		{name: "bad image", params: `{"sessionId":"active","text":"steer","content":[{"type":"image","mimeType":"image/png","data":"not-base64"}]}`, want: "not valid base64"},
		{name: "wrong image type", params: `{"sessionId":"active","text":"steer","content":[{"type":"image","mimeType":"image/png","data":"aGVsbG8="}]}`, want: "does not match mime type"},
		{name: "unknown block", params: `{"sessionId":"active","text":"steer","content":[{"type":"video"}]}`, want: "unsupported interjection content"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var output bytes.Buffer
			server := &Server{output: &output, sessions: map[string]*session{"active": {id: "active", runner: &agent.Runner{}, running: true}}}
			server.handleInterject(message{ID: json.RawMessage("1"), Params: json.RawMessage(tt.params)})
			if !strings.Contains(output.String(), tt.want) {
				t.Fatalf("response=%s want=%q", output.String(), tt.want)
			}
		})
	}
}

func TestInterjectionContentUsesFirstNonEmptyText(t *testing.T) {
	text, images, err := interjectionContent("raw", []promptBlock{
		{Type: "text", Text: "  "},
		{Type: "text", Text: "first"},
		{Type: "text", Text: "second"},
	})
	if err != nil || text != "first" || len(images) != 0 {
		t.Fatalf("text=%q images=%#v err=%v", text, images, err)
	}
}
