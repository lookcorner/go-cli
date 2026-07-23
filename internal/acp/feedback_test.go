package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/api"
	sessionlog "github.com/lookcorner/go-cli/internal/session"
	"github.com/lookcorner/go-cli/internal/tools"
	"github.com/lookcorner/go-cli/internal/workspace"
)

func TestFeedbackSlashCommandPersistsLocallyWithoutModelTurn(t *testing.T) {
	logger, err := sessionlog.NewLoggerWithID(t.TempDir(), "feedback-session")
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	runner := &agent.Runner{Logger: logger}
	runner.SubmitFeedback = func(text string) error {
		return logger.Append("user_feedback", sessionlog.UserFeedback{SessionID: logger.ID(), Text: text, ModelID: "grok-build"})
	}
	current := &session{id: logger.ID(), cwd: "/workspace", runner: runner, activePrompt: -1}
	var output bytes.Buffer
	server := &Server{output: &output, sessions: map[string]*session{current.id: current}}
	request := func(id, prompt string) []map[string]any {
		t.Helper()
		output.Reset()
		params, _ := json.Marshal(map[string]any{"sessionId": current.id, "prompt": []any{map[string]any{"type": "text", "text": prompt}}})
		server.handlePrompt(context.Background(), message{ID: json.RawMessage(id), Method: "session/prompt", Params: params})
		return decodeACPOutput(t, output.Bytes())
	}

	if messages := request("1", "/feedback"); !feedbackOutputContains(messages, "Usage: /feedback <text>") {
		t.Fatalf("usage messages=%#v", messages)
	}
	messages := request("2", "/feedback  keep local only ")
	if !feedbackOutputContains(messages, "Feedback saved locally; no feedback server is configured for this session.") || current.promptIndex != 0 {
		t.Fatalf("messages=%#v promptIndex=%d", messages, current.promptIndex)
	}
	events, err := sessionlog.Events(logger.Path(), "user_feedback")
	if err != nil || len(events) != 1 {
		t.Fatalf("events=%#v err=%v", events, err)
	}
	data := events[0].Data.(map[string]any)
	if data["sessionId"] != logger.ID() || data["text"] != "keep local only" || data["modelId"] != "grok-build" {
		t.Fatalf("feedback=%#v", data)
	}
}

func TestFeedbackSlashCommandReportsPersistenceFailure(t *testing.T) {
	current := &session{id: "feedback-failure", runner: &agent.Runner{SubmitFeedback: func(string) error { return errors.New("disk full") }}}
	var output bytes.Buffer
	server := &Server{output: &output, sessions: map[string]*session{current.id: current}}
	params := json.RawMessage(`{"sessionId":"feedback-failure","prompt":[{"type":"text","text":"/feedback text"}]}`)
	server.handlePrompt(context.Background(), message{ID: json.RawMessage("1"), Method: "session/prompt", Params: params})
	if messages := decodeACPOutput(t, output.Bytes()); !feedbackOutputContains(messages, "Feedback could not be saved locally: disk full") {
		t.Fatalf("messages=%#v", messages)
	}
}

func TestDisabledFeedbackCommandFallsThroughToModel(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry(ws, tools.PromptApprover{Mode: tools.PermissionAuto})
	defer registry.Close()
	streamer := &fixtureStreamer{results: []api.StreamResult{{ResponseID: "feedback-model", Text: "model handled feedback"}}}
	current := &session{id: "feedback-disabled", runner: &agent.Runner{Client: streamer, Tools: registry}, activePrompt: -1}
	var output bytes.Buffer
	server := &Server{output: &output, sessions: map[string]*session{current.id: current}}
	params := json.RawMessage(`{"sessionId":"feedback-disabled","prompt":[{"type":"text","text":"/feedback text"}]}`)
	server.handlePrompt(context.Background(), message{ID: json.RawMessage("1"), Method: "session/prompt", Params: params})
	server.wg.Wait()
	if len(streamer.requests) != 1 || !strings.Contains(fmt.Sprint(streamer.requests[0].Input), "/feedback text") || current.promptIndex != 1 {
		t.Fatalf("requests=%#v output=%s", streamer.requests, output.String())
	}
}

func feedbackOutputContains(messages []map[string]any, want string) bool {
	for _, item := range messages {
		params, _ := item["params"].(map[string]any)
		update, _ := params["update"].(map[string]any)
		content, _ := update["content"].(map[string]any)
		if strings.Contains(fmt.Sprint(content["text"]), want) {
			return true
		}
	}
	return false
}
