package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	runner.SubmitFeedback = func(feedback sessionlog.UserFeedback) error {
		feedback.SessionID = logger.ID()
		feedback.ModelID = "grok-build"
		return logger.Append("user_feedback", feedback)
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
	current := &session{id: "feedback-failure", runner: &agent.Runner{SubmitFeedback: func(sessionlog.UserFeedback) error { return errors.New("disk full") }}}
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

func TestFeedbackExtensionPersistsStructuredAndSimpleInputs(t *testing.T) {
	logger, err := sessionlog.NewLoggerWithID(t.TempDir(), "feedback-extension")
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	runner := &agent.Runner{}
	runner.SubmitFeedback = func(feedback sessionlog.UserFeedback) error {
		feedback.SessionID = logger.ID()
		feedback.ModelID = "grok-build"
		feedback.ResolvedModelID = "grok-4"
		return logger.Append("user_feedback", feedback)
	}
	current := &session{id: logger.ID(), cwd: "/workspace", runner: runner, promptIndex: 3}
	var output bytes.Buffer
	server := &Server{output: &output, sessions: map[string]*session{current.id: current}}

	server.handleFeedback(message{ID: json.RawMessage("1"), Method: "x.ai/feedback", Params: json.RawMessage(`{
		"session_id":"feedback-extension","client_type":"desktop","rating_type":"stars",
		"rating_value":9,"feedback_text":"accurate","feedback_categories":["quality"],
		"context_type":"turn","turnNumber":1,"request_id":"request-1","client_version":"2.0",
		"metadata":{"source":"button"},"terminal_info":{"shell":"zsh"}
	}`)})
	response := decodeACPOutput(t, output.Bytes())[0]
	if result := response["result"].(map[string]any); result["success"] != true {
		t.Fatalf("response=%#v", response)
	}

	output.Reset()
	server.handleFeedback(message{ID: json.RawMessage("2"), Method: "x.ai/feedback", Params: json.RawMessage(`{"session_id":"feedback-extension","feedback_text":"simple"}`)})
	if response := decodeACPOutput(t, output.Bytes())[0]; response["error"] != nil {
		t.Fatalf("simple response=%#v", response)
	}

	events, err := sessionlog.Events(logger.Path(), "user_feedback")
	if err != nil || len(events) != 2 {
		t.Fatalf("events=%#v err=%v", events, err)
	}
	structured := events[0].Data.(map[string]any)
	if structured["sessionId"] != logger.ID() || structured["clientType"] != "desktop" || structured["ratingType"] != "stars" || structured["ratingValue"] != float64(5) || structured["turnNumber"] != float64(1) || structured["requestId"] != "request-1" || structured["solicited"] != true || structured["contextType"] != "turn" || structured["clientVersion"] != "2.0" || structured["modelId"] != "grok-build" || structured["resolvedModelId"] != "grok-4" {
		t.Fatalf("structured feedback=%#v", structured)
	}
	if structured["metadata"].(map[string]any)["source"] != "button" || structured["terminalInfo"].(map[string]any)["shell"] != "zsh" || structured["categories"].([]any)[0] != "quality" {
		t.Fatalf("structured context=%#v", structured)
	}
	simple := events[1].Data.(map[string]any)
	if simple["text"] != "simple" || simple["clientType"] != "tui" || simple["turnNumber"] != float64(2) {
		t.Fatalf("simple feedback=%#v", simple)
	}
}

func TestFeedbackExtensionWireDispatch(t *testing.T) {
	root := t.TempDir()
	const sessionID = "018f47a2-4df1-7d5b-8c2a-1f7d9e6b3a40"
	var saved []sessionlog.UserFeedback
	server := &Server{SessionDir: t.TempDir(), Factory: func(_ context.Context, cfg SessionConfig, approver tools.Approver, _, _ io.Writer) (*agent.Runner, func(), error) {
		ws, err := workspace.Open(cfg.CWD)
		if err != nil {
			return nil, nil, err
		}
		registry := tools.NewRegistry(ws, approver)
		runner := &agent.Runner{Tools: registry, SubmitFeedback: func(feedback sessionlog.UserFeedback) error {
			saved = append(saved, feedback)
			return nil
		}}
		return runner, func() { _ = registry.Close() }, nil
	}}
	input := strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}` + "\n" +
			`{"jsonrpc":"2.0","id":2,"method":"session/new","params":{"cwd":` + fmt.Sprintf("%q", root) + `,"_meta":{"sessionId":"` + sessionID + `"}}}` + "\n" +
			`{"jsonrpc":"2.0","id":3,"method":"x.ai/feedback","params":{"session_id":"` + sessionID + `","feedback_text":"wire"}}` + "\n",
	)
	var output bytes.Buffer
	if err := server.Serve(context.Background(), input, &output); err != nil {
		t.Fatal(err)
	}
	var response map[string]any
	for _, item := range decodeACPOutput(t, output.Bytes()) {
		if item["id"] == float64(3) {
			response = item
		}
	}
	if response == nil || response["result"].(map[string]any)["success"] != true || len(saved) != 1 || saved[0].Text != "wire" {
		t.Fatalf("response=%#v saved=%#v output=%s", response, saved, output.String())
	}
}

func TestFeedbackExtensionDismissPersistsBeforeMissingCredentialsError(t *testing.T) {
	var saved []sessionlog.UserFeedback
	runner := &agent.Runner{SubmitFeedback: func(feedback sessionlog.UserFeedback) error {
		saved = append(saved, feedback)
		return nil
	}}
	current := &session{id: "feedback-dismiss", runner: runner}
	var output bytes.Buffer
	server := &Server{output: &output, sessions: map[string]*session{current.id: current}}
	server.handleFeedback(message{ID: json.RawMessage("1"), Method: "x.ai/feedback/dismiss", Params: json.RawMessage(`{"session_id":"feedback-dismiss","request_id":"request-2"}`)})
	response := decodeACPOutput(t, output.Bytes())[0]
	errorValue := response["error"].(map[string]any)
	if errorValue["message"] != "Internal error" || errorValue["data"] != "No credentials for feedback" || len(saved) != 1 || !saved[0].Dismissed || !saved[0].Solicited || saved[0].RequestID != "request-2" {
		t.Fatalf("response=%#v saved=%#v", response, saved)
	}
}

func TestFeedbackExtensionRejectsDisabledInvalidAndFailedSubmissions(t *testing.T) {
	tests := []struct {
		name   string
		runner *agent.Runner
		params string
		code   float64
		data   string
	}{
		{name: "disabled", runner: &agent.Runner{}, params: `{"session_id":"feedback-errors","feedback_text":"text"}`, code: -32603, data: feedbackDisabledMessage},
		{name: "invalid", runner: &agent.Runner{SubmitFeedback: func(sessionlog.UserFeedback) error { return nil }}, params: `{"session_id":"feedback-errors"}`, code: -32602, data: "invalid feedback parameters"},
		{name: "failed", runner: &agent.Runner{SubmitFeedback: func(sessionlog.UserFeedback) error { return errors.New("disk full") }}, params: `{"session_id":"feedback-errors","feedback_text":"text"}`, code: -32603, data: "Feedback submission failed: disk full"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			current := &session{id: "feedback-errors", runner: test.runner}
			server := &Server{output: &output, sessions: map[string]*session{current.id: current}}
			server.handleFeedback(message{ID: json.RawMessage("1"), Method: "x.ai/feedback", Params: json.RawMessage(test.params)})
			response := decodeACPOutput(t, output.Bytes())[0]
			errorValue := response["error"].(map[string]any)
			if errorValue["code"] != test.code || errorValue["data"] != test.data {
				t.Fatalf("response=%#v", response)
			}
		})
	}
}

func TestFeedbackExtensionRejectsMalformedUnknownAndDismissFailures(t *testing.T) {
	working := &agent.Runner{SubmitFeedback: func(sessionlog.UserFeedback) error { return nil }}
	failing := &agent.Runner{SubmitFeedback: func(sessionlog.UserFeedback) error { return errors.New("read only") }}
	tests := []struct {
		name, method, params, data string
		runner                     *agent.Runner
	}{
		{"malformed submit", "x.ai/feedback", `{`, "invalid feedback parameters", working},
		{"unknown submit", "x.ai/feedback", `{"session_id":"missing","feedback_text":"text"}`, "session not found: missing", nil},
		{"malformed dismiss", "x.ai/feedback/dismiss", `{`, "invalid feedback dismiss parameters", working},
		{"incomplete dismiss", "x.ai/feedback/dismiss", `{"session_id":"feedback-errors"}`, "session_id and request_id are required", working},
		{"unknown dismiss", "x.ai/feedback/dismiss", `{"session_id":"missing","request_id":"request"}`, "session not found: missing", nil},
		{"disabled dismiss", "x.ai/feedback/dismiss", `{"sessionId":"feedback-errors","requestId":"request"}`, feedbackDisabledMessage, &agent.Runner{}},
		{"failed dismiss", "x.ai/feedback/dismiss", `{"session_id":"feedback-errors","request_id":"request"}`, "Feedback submission failed: read only", failing},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			sessions := map[string]*session{}
			if test.runner != nil {
				sessions["feedback-errors"] = &session{id: "feedback-errors", runner: test.runner}
			}
			server := &Server{output: &output, sessions: sessions}
			server.handleFeedback(message{ID: json.RawMessage("1"), Method: test.method, Params: json.RawMessage(test.params)})
			errorValue := decodeACPOutput(t, output.Bytes())[0]["error"].(map[string]any)
			if errorValue["data"] != test.data {
				t.Fatalf("error=%#v", errorValue)
			}
		})
	}
}

func TestClampFeedbackRating(t *testing.T) {
	for _, test := range []struct {
		kind string
		in   int
		want int
	}{{"thumbs", -4, -1}, {"thumbs", 4, 1}, {"stars", 0, 1}, {"stars", 8, 5}, {"nps", -2, 0}, {"nps", 20, 10}, {"", 20, 20}} {
		got := clampFeedbackRating(test.kind, &test.in)
		if got == nil || *got != test.want {
			t.Errorf("kind=%q got=%v want=%d", test.kind, got, test.want)
		}
	}
	if clampFeedbackRating("stars", nil) != nil {
		t.Fatal("nil rating became non-nil")
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
