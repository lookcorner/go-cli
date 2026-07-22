package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/api"
)

type acpRecapStreamer struct {
	mu       sync.Mutex
	result   api.StreamResult
	err      error
	started  chan struct{}
	release  chan struct{}
	requests []api.ResponseRequest
}

func (s *acpRecapStreamer) CloneForCompaction(bool) api.Streamer { return s }

func (s *acpRecapStreamer) StreamResponse(ctx context.Context, request api.ResponseRequest, _ func(string)) (api.StreamResult, error) {
	s.mu.Lock()
	s.requests = append(s.requests, request)
	started, release := s.started, s.release
	s.mu.Unlock()
	if started != nil {
		select {
		case <-started:
		default:
			close(started)
		}
	}
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return api.StreamResult{}, ctx.Err()
		}
	}
	return s.result, s.err
}

func TestRecapManualSuccessIsFireAndForget(t *testing.T) {
	streamer := &acpRecapStreamer{result: api.StreamResult{Text: "Summary: We fixed the parser."}}
	current := &session{id: "recap", runner: &agent.Runner{Client: streamer, Model: "test"}, previous: "response-1", running: true, promptIndex: 2}
	var output bytes.Buffer
	server := &Server{output: &output, sessions: map[string]*session{"recap": current}}

	server.handleRecap(context.Background(), message{ID: json.RawMessage("1"), Params: json.RawMessage(`{"sessionId":"recap"}`)})
	server.wg.Wait()
	messages := decodeACPOutput(t, output.Bytes())
	if len(messages) != 2 || messages[0]["id"] != float64(1) {
		t.Fatalf("ack must precede notification: %#v", messages)
	}
	result := messages[0]["result"].(map[string]any)["result"].(map[string]any)
	update := messages[1]["params"].(map[string]any)["update"].(map[string]any)
	if result["ok"] != true || update["sessionUpdate"] != "session_recap" || update["summary"] != "We fixed the parser." || update["auto"] != false {
		t.Fatalf("messages=%#v", messages)
	}
	streamer.mu.Lock()
	requests := append([]api.ResponseRequest(nil), streamer.requests...)
	streamer.mu.Unlock()
	if len(requests) != 1 || requests[0].PreviousResponseID != "response-1" || len(requests[0].Tools) != 0 {
		t.Fatalf("requests=%#v", requests)
	}
	current.mu.Lock()
	defer current.mu.Unlock()
	if current.previous != "response-1" || !current.running || current.promptIndex != 2 || current.recapDone != nil {
		t.Fatalf("session state=%#v", current)
	}
}

func TestRecapAutoRequiresNewIdleThirdTurn(t *testing.T) {
	streamer := &acpRecapStreamer{result: api.StreamResult{Text: "Ready to continue."}}
	current := &session{id: "auto", runner: &agent.Runner{Client: streamer}, previous: "response-3", promptIndex: 3, updated: time.Now().Add(-minAutoRecapIdle)}
	var output bytes.Buffer
	server := &Server{output: &output, sessions: map[string]*session{"auto": current}}

	server.handleRecap(context.Background(), message{ID: json.RawMessage("1"), Params: json.RawMessage(`{"sessionId":"auto","auto":true}`)})
	server.wg.Wait()
	messages := decodeACPOutput(t, output.Bytes())
	if len(messages) != 2 {
		t.Fatalf("messages=%#v", messages)
	}
	update := messages[1]["params"].(map[string]any)["update"].(map[string]any)
	if update["sessionUpdate"] != "session_recap" || update["auto"] != true {
		t.Fatalf("update=%#v", update)
	}

	output.Reset()
	server.handleRecap(context.Background(), message{ID: json.RawMessage("2"), Params: json.RawMessage(`{"sessionId":"auto","auto":true}`)})
	server.wg.Wait()
	if messages := decodeACPOutput(t, output.Bytes()); len(messages) != 1 {
		t.Fatalf("same-turn auto recap should only ack: %#v", messages)
	}
}

func TestRecapAutoGateIsSilent(t *testing.T) {
	tests := []struct {
		name    string
		current *session
	}{
		{name: "too few turns", current: &session{promptIndex: 2, updated: time.Now().Add(-minAutoRecapIdle)}},
		{name: "not idle", current: &session{promptIndex: 3, updated: time.Now()}},
		{name: "running", current: &session{promptIndex: 3, updated: time.Now().Add(-minAutoRecapIdle), running: true}},
		{name: "no activity time", current: &session{promptIndex: 3}},
		{name: "recap in progress", current: &session{promptIndex: 3, updated: time.Now().Add(-minAutoRecapIdle), recapDone: make(chan struct{})}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			streamer := &acpRecapStreamer{result: api.StreamResult{Text: "unused"}}
			tt.current.id, tt.current.previous, tt.current.runner = "auto", "response", &agent.Runner{Client: streamer}
			var output bytes.Buffer
			server := &Server{output: &output, sessions: map[string]*session{"auto": tt.current}}
			server.handleRecap(context.Background(), message{ID: json.RawMessage("1"), Params: json.RawMessage(`{"sessionId":"auto","auto":true}`)})
			server.wg.Wait()
			if messages := decodeACPOutput(t, output.Bytes()); len(messages) != 1 {
				t.Fatalf("messages=%#v", messages)
			}
			if len(streamer.requests) != 0 {
				t.Fatalf("requests=%#v", streamer.requests)
			}
		})
	}
}

func TestRecapManualUnavailableAndConcurrent(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	streamer := &acpRecapStreamer{result: api.StreamResult{Text: "first recap"}, started: started, release: release}
	current := &session{id: "recap", runner: &agent.Runner{Client: streamer}, previous: "response", promptIndex: 1}
	var output bytes.Buffer
	server := &Server{output: &output, sessions: map[string]*session{"recap": current}}

	server.handleRecap(context.Background(), message{ID: json.RawMessage("1"), Params: json.RawMessage(`{"sessionId":"recap"}`)})
	<-started
	server.handleRecap(context.Background(), message{ID: json.RawMessage("2"), Params: json.RawMessage(`{"sessionId":"recap"}`)})
	close(release)
	server.wg.Wait()
	messages := decodeACPOutput(t, output.Bytes())
	if len(messages) != 4 || messages[0]["id"] != float64(1) || messages[1]["id"] != float64(2) {
		t.Fatalf("messages=%#v", messages)
	}
	update := messages[2]["params"].(map[string]any)["update"].(map[string]any)
	if update["sessionUpdate"] != "session_recap_unavailable" {
		t.Fatalf("concurrent update=%#v", update)
	}
	if len(streamer.requests) != 1 {
		t.Fatalf("requests=%#v", streamer.requests)
	}

	var failed bytes.Buffer
	failing := &Server{output: &failed, sessions: map[string]*session{"empty": {id: "empty", runner: &agent.Runner{Client: &acpRecapStreamer{err: errors.New("offline")}}, previous: "response", promptIndex: 1}}}
	failing.handleRecap(context.Background(), message{ID: json.RawMessage("3"), Params: json.RawMessage(`{"sessionId":"empty"}`)})
	failing.wg.Wait()
	failedMessages := decodeACPOutput(t, failed.Bytes())
	failedUpdate := failedMessages[1]["params"].(map[string]any)["update"].(map[string]any)
	if failedUpdate["sessionUpdate"] != "session_recap_unavailable" {
		t.Fatalf("failed messages=%#v", failedMessages)
	}

	failed.Reset()
	noHistory := &Server{output: &failed, sessions: map[string]*session{"empty": {id: "empty", runner: &agent.Runner{Client: &acpRecapStreamer{}}}}}
	noHistory.handleRecap(context.Background(), message{ID: json.RawMessage("4"), Params: json.RawMessage(`{"sessionId":"empty"}`)})
	noHistory.wg.Wait()
	if messages := decodeACPOutput(t, failed.Bytes()); len(messages) != 2 || messages[1]["params"].(map[string]any)["update"].(map[string]any)["sessionUpdate"] != "session_recap_unavailable" {
		t.Fatalf("no-history messages=%#v", messages)
	}
}

func TestRecapAutoFailureIsSilentAndRetryable(t *testing.T) {
	streamer := &acpRecapStreamer{err: errors.New("offline")}
	current := &session{id: "auto", runner: &agent.Runner{Client: streamer}, previous: "response", promptIndex: 3, updated: time.Now().Add(-minAutoRecapIdle)}
	var output bytes.Buffer
	server := &Server{output: &output, sessions: map[string]*session{"auto": current}}
	request := message{ID: json.RawMessage("1"), Params: json.RawMessage(`{"sessionId":"auto","auto":true}`)}

	server.handleRecap(context.Background(), request)
	server.wg.Wait()
	if messages := decodeACPOutput(t, output.Bytes()); len(messages) != 1 {
		t.Fatalf("failed auto recap should only ack: %#v", messages)
	}
	current.mu.Lock()
	watermark := current.lastRecapPrompt
	current.mu.Unlock()
	if watermark != 0 {
		t.Fatalf("failed auto recap advanced watermark to %d", watermark)
	}
}

func TestRecapCloseCancelsAndWaits(t *testing.T) {
	started := make(chan struct{})
	streamer := &acpRecapStreamer{started: started, release: make(chan struct{})}
	closed := false
	current := &session{id: "closing", runner: &agent.Runner{Client: streamer}, previous: "response", promptIndex: 1, close: func() { closed = true }}
	var output bytes.Buffer
	server := &Server{output: &output, sessions: map[string]*session{"closing": current}}
	server.handleRecap(context.Background(), message{ID: json.RawMessage("1"), Params: json.RawMessage(`{"sessionId":"closing"}`)})
	<-started
	if !server.closeSession("closing") || !closed {
		t.Fatal("session did not close after cancelling recap")
	}
	server.wg.Wait()
	if messages := decodeACPOutput(t, output.Bytes()); len(messages) != 2 || messages[0]["id"] != float64(1) || messages[1]["method"] != "x.ai/sessions/changed" || messages[1]["params"].(map[string]any)["removed"].([]any)[0] != "closing" {
		t.Fatalf("cancelled recap close output: %#v", messages)
	}
}

func TestRecapServerShutdownCancelsAndWaits(t *testing.T) {
	started := make(chan struct{})
	streamer := &acpRecapStreamer{started: started, release: make(chan struct{})}
	closed := false
	current := &session{id: "shutdown", runner: &agent.Runner{Client: streamer}, previous: "response", promptIndex: 1, close: func() { closed = true }}
	var output bytes.Buffer
	server := &Server{output: &output, sessions: map[string]*session{"shutdown": current}}
	server.handleRecap(context.Background(), message{ID: json.RawMessage("1"), Params: json.RawMessage(`{"sessionId":"shutdown"}`)})
	<-started

	server.closeAll()
	server.wg.Wait()
	if !closed {
		t.Fatal("session runtime closed before recap shutdown completed")
	}
	if messages := decodeACPOutput(t, output.Bytes()); len(messages) != 1 {
		t.Fatalf("shutdown recap should only ack: %#v", messages)
	}
}

func TestRecapRejectsInvalidUnknownAndClosedSessions(t *testing.T) {
	tests := []struct {
		name, params, want string
		sessions           map[string]*session
	}{
		{name: "invalid", params: `{}`, want: "sessionId is required"},
		{name: "unknown", params: `{"sessionId":"missing"}`, want: "session not found"},
		{name: "closed", params: `{"sessionId":"closed"}`, want: "session is closed", sessions: map[string]*session{"closed": {closed: true}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var output bytes.Buffer
			server := &Server{output: &output, sessions: tt.sessions}
			server.handleRecap(context.Background(), message{ID: json.RawMessage("1"), Params: json.RawMessage(tt.params)})
			if !bytes.Contains(output.Bytes(), []byte(tt.want)) {
				t.Fatalf("response=%s", output.String())
			}
		})
	}
}
