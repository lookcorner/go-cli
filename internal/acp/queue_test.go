package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/api"
	"github.com/lookcorner/go-cli/internal/tools"
	"github.com/lookcorner/go-cli/internal/workspace"
)

type queuedPromptStreamer struct {
	mu       sync.Mutex
	requests []api.ResponseRequest
	started  chan int
	release  chan api.StreamResult
}

func (f *queuedPromptStreamer) StreamResponse(ctx context.Context, request api.ResponseRequest, _ func(string)) (api.StreamResult, error) {
	f.mu.Lock()
	index := len(f.requests)
	f.requests = append(f.requests, request)
	f.mu.Unlock()
	f.started <- index
	select {
	case result := <-f.release:
		return result, nil
	case <-ctx.Done():
		return api.StreamResult{}, ctx.Err()
	}
}

func TestPromptQueueRunsFIFOAndPreservesResponseChain(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry(ws, tools.PromptApprover{Mode: tools.PermissionAuto})
	defer registry.Close()
	streamer := &queuedPromptStreamer{started: make(chan int, 3), release: make(chan api.StreamResult, 3)}
	current := &session{
		id: "fifo", ctx: context.Background(), cwd: t.TempDir(), activePrompt: -1,
		runner:    &agent.Runner{Client: streamer, Tools: registry, Model: "test"},
		wakeQueue: []syntheticWake{{id: "background", prompt: "background wake"}},
	}
	var output bytes.Buffer
	server := &Server{output: &output, sessions: map[string]*session{"fifo": current}}

	for index, text := range []string{"one", "two", "three"} {
		params, _ := json.Marshal(map[string]any{
			"sessionId": "fifo", "prompt": []any{map[string]any{"type": "text", "text": text}},
			"_meta": map[string]any{"promptId": "p" + text, "clientIdentifier": "client"},
		})
		server.handlePrompt(context.Background(), message{ID: json.RawMessage([]byte{'1' + byte(index)}), Method: "session/prompt", Params: params})
		if index == 0 {
			waitPromptStart(t, streamer.started, 0)
		}
	}
	select {
	case index := <-streamer.started:
		t.Fatalf("queued prompt %d started before the active prompt completed", index)
	default:
	}

	streamer.release <- api.StreamResult{ResponseID: "r1"}
	waitPromptStart(t, streamer.started, 1)
	streamer.release <- api.StreamResult{ResponseID: "r2"}
	waitPromptStart(t, streamer.started, 2)
	streamer.release <- api.StreamResult{ResponseID: "r3"}
	waitPromptStart(t, streamer.started, 3)
	streamer.release <- api.StreamResult{ResponseID: "wake"}
	server.wg.Wait()

	streamer.mu.Lock()
	requests := append([]api.ResponseRequest(nil), streamer.requests...)
	streamer.mu.Unlock()
	if len(requests) != 4 || requests[0].PreviousResponseID != "" || requests[1].PreviousResponseID != "r1" || requests[2].PreviousResponseID != "r2" || requests[3].PreviousResponseID != "r3" {
		t.Fatalf("requests=%#v", requests)
	}
	input, _ := json.Marshal(requests[3].Input)
	if !bytes.Contains(input, []byte("background wake")) {
		t.Fatalf("background wake ran out of order: %#v", requests)
	}
	current.mu.Lock()
	queued, running := len(current.promptQueue), current.running
	current.mu.Unlock()
	if queued != 0 || running {
		t.Fatalf("queued=%d running=%v", queued, running)
	}

	responses := make(map[float64]string)
	for _, item := range decodeACPOutput(t, output.Bytes()) {
		if id, ok := item["id"].(float64); ok {
			if result, ok := item["result"].(map[string]any); ok {
				responses[id], _ = result["stopReason"].(string)
			}
		}
	}
	for _, id := range []float64{1, 2, 3} {
		if responses[id] != "end_turn" {
			t.Fatalf("responses=%#v", responses)
		}
	}
}

func TestPromptQueueUpdatesAreAuthoritative(t *testing.T) {
	var output bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	current := &session{id: "queue", running: true, cancel: cancel, runningPromptID: "running"}
	server := &Server{output: &output, sessions: map[string]*session{"queue": current}}

	queuePromptForTest(t, server, current, 1, "p1", "alpha", "owner-a", "Display alpha", false)
	queuePromptForTest(t, server, current, 2, "p2", "beta", "owner-b", "", false)
	queuePromptForTest(t, server, current, 3, "p3", "gamma", "owner-a", "", false)
	current.mu.Lock()
	current.promptQueue[1].request.Prompt = append(current.promptQueue[1].request.Prompt, promptBlock{Type: "image", URI: "https://example.invalid/image.png"})
	current.mu.Unlock()
	server.handleQueueUpdate(queueNotification("x.ai/queue/edit", map[string]any{
		"sessionId": "queue", "id": "p2", "newText": "edited beta", "clientIdentifier": "editor",
	}))
	server.handleQueueUpdate(queueNotification("x.ai/queue/reorder", map[string]any{
		"sessionId": "queue", "orderedIds": []string{"p3"},
	}))
	server.handleQueueUpdate(queueNotification("x.ai/queue/remove", map[string]any{
		"sessionId": "queue", "id": "p1", "expectedVersion": 1, "owner": "owner-a",
	}))
	server.handleQueueUpdate(queueNotification("x.ai/queue/remove", map[string]any{
		"sessionId": "queue", "id": "p1", "expectedVersion": 0, "owner": "wrong-owner",
	}))
	server.handleQueueUpdate(queueNotification("x.ai/queue/remove", map[string]any{
		"sessionId": "queue", "id": "p1", "expectedVersion": 0, "owner": "owner-a",
	}))

	current.mu.Lock()
	if len(current.promptQueue) != 2 || current.promptQueue[0].id != "p3" || current.promptQueue[1].id != "p2" {
		t.Fatalf("queue=%#v", current.promptQueue)
	}
	edited := current.promptQueue[1]
	current.mu.Unlock()
	if edited.version != 1 || edited.lastEditor != "editor" || edited.text != "edited beta" || len(edited.request.Prompt) != 2 || edited.request.Prompt[1].Type != "image" {
		t.Fatalf("edited=%#v", edited)
	}

	server.handleQueueUpdate(queueNotification("x.ai/queue/interject", map[string]any{
		"sessionId": "queue", "id": "p2", "expectedVersion": 1, "owner": "owner-b", "newText": "send beta now",
	}))
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("send-now did not cancel the interruptible active turn")
	}
	current.mu.Lock()
	if len(current.promptQueue) != 2 || current.promptQueue[0].id != "p2" || !current.promptQueue[0].sendNow || current.promptQueue[0].version != 2 {
		t.Fatalf("interjected queue=%#v", current.promptQueue)
	}
	current.mu.Unlock()

	server.handleQueueUpdate(queueNotification("x.ai/queue/clear", map[string]any{"sessionId": "queue", "owner": "owner-a"}))
	current.mu.Lock()
	if len(current.promptQueue) != 1 || current.promptQueue[0].id != "p2" {
		t.Fatalf("owner clear queue=%#v", current.promptQueue)
	}
	current.mu.Unlock()
	server.handleQueueUpdate(queueNotification("x.ai/queue/clear", map[string]any{"sessionId": "queue"}))
	current.mu.Lock()
	remaining := len(current.promptQueue)
	current.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("remaining=%d", remaining)
	}

	messages := decodeACPOutput(t, output.Bytes())
	foundDisplay, cancelled := false, map[float64]bool{}
	for _, item := range messages {
		if item["method"] == "x.ai/queue/changed" {
			params := item["params"].(map[string]any)
			if params["runningPromptId"] == "running" {
				for _, raw := range params["entries"].([]any) {
					entry := raw.(map[string]any)
					foundDisplay = foundDisplay || entry["id"] == "p1" && entry["text"] == "Display alpha" && entry["position"] == float64(0)
				}
			}
		}
		if id, ok := item["id"].(float64); ok {
			result, _ := item["result"].(map[string]any)
			cancelled[id] = result["stopReason"] == "cancelled"
		}
	}
	if !foundDisplay || !cancelled[1] || !cancelled[2] || !cancelled[3] {
		t.Fatalf("foundDisplay=%v cancelled=%#v messages=%#v", foundDisplay, cancelled, messages)
	}
}

func TestClosingSessionCancelsQueuedPrompt(t *testing.T) {
	var output bytes.Buffer
	closed := false
	current := &session{id: "closing-queue", running: true, close: func() { closed = true }}
	server := &Server{output: &output, sessions: map[string]*session{"closing-queue": current}}
	queuePromptForTest(t, server, current, 7, "queued", "wait", "client", "", false)
	current.mu.Lock()
	current.running = false
	current.mu.Unlock()
	if !server.closeSession("closing-queue") || !closed {
		t.Fatal("session was not closed")
	}
	messages := decodeACPOutput(t, output.Bytes())
	last := messages[len(messages)-1]
	if last["id"] != float64(7) || last["result"].(map[string]any)["stopReason"] != "cancelled" {
		t.Fatalf("messages=%#v", messages)
	}
}

func TestCancelContinuesWithNextQueuedPrompt(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry(ws, tools.PromptApprover{Mode: tools.PermissionAuto})
	defer registry.Close()
	streamer := &queuedPromptStreamer{started: make(chan int, 2), release: make(chan api.StreamResult, 1)}
	current := &session{
		id: "cancel-next", ctx: context.Background(), cwd: t.TempDir(), activePrompt: -1,
		runner: &agent.Runner{Client: streamer, Tools: registry, Model: "test"},
	}
	var output bytes.Buffer
	server := &Server{output: &output, sessions: map[string]*session{"cancel-next": current}}
	for id, text := range []string{"active", "next"} {
		params, _ := json.Marshal(map[string]any{
			"sessionId": "cancel-next", "prompt": []any{map[string]any{"type": "text", "text": text}},
			"_meta": map[string]any{"promptId": text},
		})
		server.handlePrompt(context.Background(), message{ID: json.RawMessage([]byte{byte('1' + id)}), Method: "session/prompt", Params: params})
		if id == 0 {
			waitPromptStart(t, streamer.started, 0)
		}
	}
	server.handleCancel(json.RawMessage(`{"sessionId":"cancel-next"}`))
	waitPromptStart(t, streamer.started, 1)
	streamer.release <- api.StreamResult{ResponseID: "next-response"}
	server.wg.Wait()

	responses := map[float64]string{}
	for _, item := range decodeACPOutput(t, output.Bytes()) {
		if id, ok := item["id"].(float64); ok {
			if result, ok := item["result"].(map[string]any); ok {
				responses[id], _ = result["stopReason"].(string)
			}
		}
	}
	if responses[1] != "cancelled" || responses[2] != "end_turn" {
		t.Fatalf("responses=%#v", responses)
	}
}

func TestCloseAllCancelsEveryQueuedPrompt(t *testing.T) {
	var output bytes.Buffer
	closed := false
	current := &session{
		id: "close-all", promptQueue: []queuedPrompt{
			{incoming: message{ID: json.RawMessage("1")}},
			{incoming: message{ID: json.RawMessage("2")}},
		},
		close: func() { closed = true },
	}
	server := &Server{output: &output, sessions: map[string]*session{"close-all": current}}
	server.closeAll()
	if !closed {
		t.Fatal("session resources were not closed")
	}
	cancelled := map[float64]bool{}
	for _, item := range decodeACPOutput(t, output.Bytes()) {
		id, _ := item["id"].(float64)
		result, _ := item["result"].(map[string]any)
		cancelled[id] = result["stopReason"] == "cancelled"
	}
	if !cancelled[1] || !cancelled[2] {
		t.Fatalf("cancelled=%#v", cancelled)
	}
}

func TestPromptQueueGeneratesIDsAndPrioritizesSendNow(t *testing.T) {
	var output bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	current := &session{id: "generated", running: true, cancel: cancel, runningPromptID: "active"}
	server := &Server{output: &output, sessions: map[string]*session{"generated": current}}
	ordinary := promptRequest{SessionID: "generated", Prompt: []promptBlock{{Type: "text", Text: "ordinary"}}}
	if !server.queuePrompt(current, message{ID: json.RawMessage("1")}, &ordinary, "ordinary") || promptID(ordinary.Meta) == "" {
		t.Fatalf("generated meta=%#v", ordinary.Meta)
	}
	queuePromptForTest(t, server, current, 2, "urgent", "urgent", "", "", true)
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("send-now did not cancel the active turn")
	}
	current.mu.Lock()
	defer current.mu.Unlock()
	if len(current.promptQueue) != 2 || current.promptQueue[0].id != "urgent" || current.promptQueue[1].id != promptID(ordinary.Meta) {
		t.Fatalf("queue=%#v", current.promptQueue)
	}
}

func waitPromptStart(t *testing.T, started <-chan int, want int) {
	t.Helper()
	select {
	case got := <-started:
		if got != want {
			t.Fatalf("started=%d want=%d", got, want)
		}
	case <-time.After(time.Second):
		t.Fatalf("prompt %d did not start", want)
	}
}

func queuePromptForTest(t *testing.T, server *Server, current *session, id int, promptID, text, owner, display string, sendNow bool) {
	t.Helper()
	meta := map[string]any{"promptId": promptID, "clientIdentifier": owner, "sendNow": sendNow}
	blockMeta := map[string]any{}
	if display != "" {
		blockMeta["displayText"] = display
	}
	request := promptRequest{SessionID: current.id, Meta: meta, Prompt: []promptBlock{{Type: "text", Text: text, Meta: blockMeta}}}
	if !server.queuePrompt(current, message{ID: json.RawMessage([]byte{byte('0' + id)})}, &request, text) {
		t.Fatal("prompt was not queued")
	}
}

func queueNotification(method string, params any) message {
	raw, _ := json.Marshal(params)
	return message{Method: method, Params: raw}
}
