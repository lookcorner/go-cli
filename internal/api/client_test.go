package api

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseSSETextAndToolCall(t *testing.T) {
	toolItem := map[string]any{
		"type": "function_call", "id": "fc_1", "call_id": "call_1",
		"name": "read_file", "arguments": "{\"path\":\"README.md\"}",
	}
	stream := strings.Join([]string{
		"event: response.output_text.delta",
		sseLine(t, map[string]any{"type": "response.output_text.delta", "delta": "hello "}),
		"",
		sseLine(t, map[string]any{"type": "response.output_text.delta", "delta": "world"}),
		"",
		sseLine(t, map[string]any{"type": "response.output_item.done", "item": toolItem}),
		"",
		sseLine(t, map[string]any{
			"type":     "response.completed",
			"response": map[string]any{"id": "resp_1", "output": []any{toolItem}},
		}),
		"",
		"data: [DONE]",
	}, "\n")
	var streamed strings.Builder
	result, err := parseSSE(strings.NewReader(stream), func(delta string) { streamed.WriteString(delta) })
	if err != nil {
		t.Fatal(err)
	}
	if result.ResponseID != "resp_1" || result.Text != "hello world" || streamed.String() != result.Text {
		t.Fatalf("unexpected result: %#v, streamed=%q", result, streamed.String())
	}
	if len(result.ToolCalls) != 1 {
		t.Fatalf("expected deduplicated tool call, got %#v", result.ToolCalls)
	}
	if result.ToolCalls[0].CallID != "call_1" || result.ToolCalls[0].Name != "read_file" {
		t.Fatalf("unexpected tool call: %#v", result.ToolCalls[0])
	}
}

func TestParseSSEError(t *testing.T) {
	line := sseLine(t, map[string]any{
		"type": "error", "error": map[string]any{"message": "bad request"},
	})
	_, err := parseSSE(strings.NewReader(line+"\n"), nil)
	if err == nil || !strings.Contains(err.Error(), "bad request") {
		t.Fatalf("expected API error, got %v", err)
	}
}

func sseLine(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return "data: " + string(encoded)
}
