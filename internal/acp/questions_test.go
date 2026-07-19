package acp

import (
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/lookcorner/go-cli/internal/tools"
)

func TestAskUserQuestionWireContract(t *testing.T) {
	reader, writer := io.Pipe()
	defer reader.Close()
	server := &Server{output: writer, pendingQuestion: make(map[string]chan userQuestionResult)}
	type result struct {
		response tools.UserQuestionResponse
		err      error
	}
	done := make(chan result, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() {
		response, err := server.RequestUserQuestion(ctx, "sess-1", tools.UserQuestionRequest{
			ToolCallID: "call-1", Mode: "plan",
			Questions: []tools.UserQuestion{{
				Question: "Which database?", MultiSelect: true,
				Options: []tools.UserQuestionOption{{Label: "SQLite", Description: "Local file"}},
			}},
		})
		done <- result{response: response, err: err}
	}()

	var request message
	if err := json.NewDecoder(reader).Decode(&request); err != nil {
		t.Fatal(err)
	}
	if request.Method != "x.ai/session_notification" {
		t.Fatalf("pending method=%q", request.Method)
	}
	var pending map[string]any
	if err := json.Unmarshal(request.Params, &pending); err != nil {
		t.Fatal(err)
	}
	pendingUpdate := pending["update"].(map[string]any)
	if pendingUpdate["sessionUpdate"] != "pending_interaction" || pendingUpdate["tool_call_id"] != "call-1" || pendingUpdate["kind"] != "question" {
		t.Fatalf("pending=%#v", pending)
	}
	if err := json.NewDecoder(reader).Decode(&request); err != nil {
		t.Fatal(err)
	}
	if request.Method != "x.ai/ask_user_question" {
		t.Fatalf("method=%q", request.Method)
	}
	var params map[string]any
	if err := json.Unmarshal(request.Params, &params); err != nil {
		t.Fatal(err)
	}
	questions := params["questions"].([]any)
	question := questions[0].(map[string]any)
	if params["sessionId"] != "sess-1" || params["toolCallId"] != "call-1" || params["mode"] != "plan" || question["multiSelect"] != true {
		t.Fatalf("params=%#v", params)
	}
	server.handleClientResponse(message{ID: request.ID, Result: json.RawMessage(`{
		"outcome":"accepted",
		"answers":{"Which database?":"SQLite"},
		"annotations":{"Which database?":{"notes":"keep it local"}}
	}`)})
	var resolved message
	if err := json.NewDecoder(reader).Decode(&resolved); err != nil {
		t.Fatal(err)
	}
	var resolvedParams map[string]any
	if err := json.Unmarshal(resolved.Params, &resolvedParams); err != nil {
		t.Fatal(err)
	}
	resolvedUpdate := resolvedParams["update"].(map[string]any)
	if resolved.Method != "x.ai/session_notification" || resolvedUpdate["sessionUpdate"] != "interaction_resolved" || resolvedUpdate["tool_call_id"] != "call-1" {
		t.Fatalf("resolved=%#v", resolved)
	}
	got := <-done
	if got.err != nil || got.response.Outcome != "accepted" || len(got.response.Answers["Which database?"]) != 1 || got.response.Answers["Which database?"][0] != "SQLite" || got.response.Annotations["Which database?"].Notes != "keep it local" {
		t.Fatalf("response=%#v err=%v", got.response, got.err)
	}
	_ = writer.Close()
}
