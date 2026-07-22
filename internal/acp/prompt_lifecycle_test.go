package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/api"
	"github.com/lookcorner/go-cli/internal/tools"
	"github.com/lookcorner/go-cli/internal/workspace"
)

type promptErrorStreamer struct{ err error }

func (f promptErrorStreamer) StreamResponse(context.Context, api.ResponseRequest, func(string)) (api.StreamResult, error) {
	return api.StreamResult{}, f.err
}

func TestPromptLifecyclePublishesCompletionAndResponseMeta(t *testing.T) {
	server, current, output := promptLifecycleFixture(t, &fixtureStreamer{results: []api.StreamResult{{
		ResponseID: "response-1", Text: "done", Usage: api.Usage{InputTokens: 9, OutputTokens: 3, TotalTokens: 12, CachedReadTokens: 4, ReasoningTokens: 2},
	}}})
	server.handlePrompt(context.Background(), message{
		ID: json.RawMessage("7"), Method: "session/prompt",
		Params: json.RawMessage(`{"sessionId":"lifecycle","prompt":[{"type":"text","text":"work"}],"_meta":{"promptId":"prompt-7","turnId":42}}`),
	})
	server.wg.Wait()

	messages := decodeACPOutput(t, output.Bytes())
	completeIndex, responseIndex := -1, -1
	for index, item := range messages {
		if item["method"] == "x.ai/session/prompt_complete" {
			completeIndex = index
			assertPromptComplete(t, item, current.id, "prompt-7", "end_turn")
			params := item["params"].(map[string]any)
			if params["turnId"] != float64(42) {
				t.Fatalf("completion=%#v", item)
			}
		}
		if item["id"] == float64(7) {
			responseIndex = index
			result := item["result"].(map[string]any)
			meta := result["_meta"].(map[string]any)
			if result["stopReason"] != "end_turn" || meta["sessionId"] != current.id || meta["requestId"] != "prompt-7" || meta["promptId"] != "prompt-7" || meta["modelId"] != "test-model" || meta["totalTokens"] != float64(12) || meta["inputTokens"] != float64(9) || meta["outputTokens"] != float64(3) || meta["cachedReadTokens"] != float64(4) || meta["reasoningTokens"] != float64(2) {
				t.Fatalf("response=%#v", item)
			}
		}
	}
	if completeIndex < 0 || responseIndex <= completeIndex {
		t.Fatalf("messages=%#v", messages)
	}
}

func TestPromptLifecyclePublishesErrorsAndSlashValidation(t *testing.T) {
	server, current, output := promptLifecycleFixture(t, promptErrorStreamer{err: errors.New("model unavailable")})
	server.handlePrompt(context.Background(), message{
		ID: json.RawMessage("1"), Method: "session/prompt",
		Params: json.RawMessage(`{"sessionId":"lifecycle","prompt":[{"type":"text","text":"work"}],"_meta":{"promptId":"failed"}}`),
	})
	server.wg.Wait()
	messages := decodeACPOutput(t, output.Bytes())
	assertPromptFailure(t, messages, current.id, "failed", "model unavailable")

	output.Reset()
	server.handlePrompt(context.Background(), message{
		ID: json.RawMessage("2"), Method: "session/prompt",
		Params: json.RawMessage(`{"sessionId":"lifecycle","prompt":[{"type":"text","text":"/compact"}],"_meta":{"promptId":"compact"}}`),
	})
	assertPromptFailure(t, decodeACPOutput(t, output.Bytes()), current.id, "compact", "no completed response is available to compact")
}

func promptLifecycleFixture(t *testing.T, streamer api.Streamer) (*Server, *session, *bytes.Buffer) {
	t.Helper()
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry(ws, tools.PromptApprover{Mode: tools.PermissionAuto})
	t.Cleanup(func() { _ = registry.Close() })
	current := &session{
		id: "lifecycle", ctx: context.Background(), cwd: t.TempDir(), activePrompt: -1,
		runner: &agent.Runner{Client: streamer, Tools: registry, Model: "test-model"},
	}
	output := &bytes.Buffer{}
	return &Server{output: output, sessions: map[string]*session{current.id: current}}, current, output
}

func assertPromptFailure(t *testing.T, messages []map[string]any, sessionID, promptID, detail string) {
	t.Helper()
	foundComplete, foundError := false, false
	for _, item := range messages {
		if item["method"] == "x.ai/session/prompt_complete" {
			params := item["params"].(map[string]any)
			foundComplete = params["sessionId"] == sessionID && params["promptId"] == promptID && params["stopReason"] == "error" && params["agentResult"] == detail
		}
		if wireError, ok := item["error"].(map[string]any); ok {
			foundError = wireError["message"] == detail
		}
	}
	if !foundComplete || !foundError {
		t.Fatalf("messages=%#v", messages)
	}
}
