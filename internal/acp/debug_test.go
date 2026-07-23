package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"testing"

	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/api"
	sessionlog "github.com/lookcorner/go-cli/internal/session"
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

func TestDebugTriggerFeedbackNotifiesReturnsAndPersists(t *testing.T) {
	logger, err := sessionlog.NewLoggerWithID(t.TempDir(), "debug-feedback")
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	current := &session{id: logger.ID(), runner: &agent.Runner{Logger: logger}}
	var output bytes.Buffer
	server := &Server{output: &output, sessions: map[string]*session{current.id: current}}
	server.handleDebug(message{ID: json.RawMessage("1"), Method: "x.ai/debug/trigger_feedback", Params: json.RawMessage(`{"session_id":"debug-feedback"}`)})

	messages := decodeACPOutput(t, output.Bytes())
	if len(messages) != 2 || messages[0]["method"] != "x.ai/session_notification" || messages[1]["id"] != float64(1) {
		t.Fatalf("messages=%#v", messages)
	}
	update := messages[0]["params"].(map[string]any)["update"].(map[string]any)
	result := messages[1]["result"].(map[string]any)["result"].(map[string]any)
	requestID := result["request_id"].(string)
	if !isUUIDv7(requestID) || update["request_id"] != requestID || update["sessionUpdate"] != "feedback_request" || result["tier"] != "tier1" || result["thumbs"] != true || result["text"] != true || result["stars"] != false || result["dismissible"] != true {
		t.Fatalf("update=%#v result=%#v", update, result)
	}
	if _, present := result["sessionUpdate"]; present {
		t.Fatalf("response leaked update discriminator: %#v", result)
	}
	events, err := sessionlog.Events(logger.Path(), "xai_session_notification")
	if err != nil || len(events) != 1 {
		t.Fatalf("events=%#v err=%v", events, err)
	}
	persisted := events[0].Data.(map[string]any)["update"].(map[string]any)
	if persisted["sessionUpdate"] != "feedback_request" || persisted["request_id"] != requestID {
		t.Fatalf("persisted=%#v", persisted)
	}
}

func TestDebugTriggerFeedbackTierAndModeShapes(t *testing.T) {
	current := &session{id: "debug-feedback", runner: &agent.Runner{}}
	var output bytes.Buffer
	server := &Server{output: &output, sessions: map[string]*session{current.id: current}}
	tiers := []struct {
		tier, trigger, prompt, reason string
	}{
		{"tier1", "tier1_engagement", "You've been using Grok Code productively! Would you mind sharing quick feedback?", "Tier 1: Sustained engagement (turns=0, tools=0, compactions=0, no cancellations)"},
		{"tier2", "tier2_complex_recovery", "You've worked through a complex session. Your feedback would help us improve.", "Tier 2: Complex session with errors (turns=0, tools=0, compactions=0, errors=0)"},
		{"tier3", "tier3_friction_recovery", "Thanks for sticking with us through that session. Got a moment to share feedback?", "Tier 3: Recovery from friction (turns=0, cancellations=0, reverted=false)"},
	}
	for index, test := range tiers {
		output.Reset()
		params, _ := json.Marshal(map[string]any{"sessionId": current.id, "tier": test.tier, "mode": "thumbs"})
		server.handleDebug(message{ID: json.RawMessage("1"), Method: "x.ai/debug/trigger_feedback", Params: params})
		result := decodeACPOutput(t, output.Bytes())[1]["result"].(map[string]any)["result"].(map[string]any)
		if result["tier"] != test.tier || result["trigger_type"] != test.trigger || result["prompt"] != test.prompt || result["trigger_reason"] != test.reason {
			t.Fatalf("tier[%d]=%#v", index, result)
		}
	}
	modes := map[string][3]bool{
		"thumbs": {false, true, false}, "stars": {true, false, false}, "text": {false, false, true},
		"thumbs_text": {false, true, true}, "stars_text": {true, false, true},
	}
	for mode, want := range modes {
		output.Reset()
		params, _ := json.Marshal(map[string]any{"sessionId": current.id, "mode": mode})
		server.handleDebug(message{ID: json.RawMessage("1"), Method: "x.ai/debug/trigger_feedback", Params: params})
		result := decodeACPOutput(t, output.Bytes())[1]["result"].(map[string]any)["result"].(map[string]any)
		if result["stars"] != want[0] || result["thumbs"] != want[1] || result["text"] != want[2] {
			t.Fatalf("mode=%s result=%#v", mode, result)
		}
	}
}

func TestDebugTriggerFeedbackValidation(t *testing.T) {
	var output bytes.Buffer
	server := &Server{output: &output, sessions: map[string]*session{
		"debug-feedback": {id: "debug-feedback", runner: &agent.Runner{}},
		"closed":         {id: "closed", runner: &agent.Runner{}, closed: true},
	}}
	tests := []struct{ params, data string }{
		{`{`, "invalid debug feedback parameters"},
		{`{}`, "sessionId required"},
		{`{"sessionId":"debug-feedback","tier":"tier4"}`, `unknown tier: "tier4" (expected tier1/tier2/tier3)`},
		{`{"sessionId":"debug-feedback","mode":"score"}`, `unknown mode: "score" (expected thumbs/stars/text/thumbs_text/stars_text)`},
		{`{"sessionId":"missing"}`, "session not found: missing"},
		{`{"sessionId":"closed"}`, "session not found: closed"},
	}
	for index, test := range tests {
		output.Reset()
		server.handleDebug(message{ID: json.RawMessage("1"), Method: "x.ai/debug/trigger_feedback", Params: json.RawMessage(test.params)})
		errorValue := decodeACPOutput(t, output.Bytes())[0]["error"].(map[string]any)
		if errorValue["code"] != float64(-32602) || errorValue["data"] != test.data {
			t.Fatalf("case[%d]=%#v", index, errorValue)
		}
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

	output.Reset()
	input = bytes.NewBufferString(`{"jsonrpc":"2.0","id":4,"method":"x.ai/debug/trigger_feedback","params":{"sessionId":"missing"}}`)
	if err := routed.Serve(context.Background(), input, &output); err != nil {
		t.Fatal(err)
	}
	messages = decodeACPOutput(t, output.Bytes())
	if len(messages) != 1 || messages[0]["id"] != float64(4) || messages[0]["error"].(map[string]any)["data"] != "session not found: missing" {
		t.Fatalf("trigger route messages=%#v", messages)
	}
}
