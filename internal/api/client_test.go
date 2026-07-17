package api

import (
	"strings"
	"testing"
)

func TestParseSSETextAndToolCall(t *testing.T) {
	stream := strings.Join([]string{
		`event: response.output_text.delta`,
		`data: {"type":"response.output_text.delta","delta":"hello "}`,
		``,
		`data: {"type":"response.output_text.delta","delta":"world"}`,
		``,
		`data: {"type":"response.output_item.done","item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"read_file","arguments":"{\"path\":\"README.md\"}"}}`,
		``,
		`data: {"type":"response.completed","response":{"id":"resp_1","output":[{"type":"function_call","id":"fc_1","call_id":"call_1","name":"read_file","arguments":"{\"path\":\"README.md\"}"}]}}`,
		``,
		`data: [DONE]`,
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
	_, err := parseSSE(strings.NewReader("data: {\"type\":\"error\",\"error\":{\"message\":\"bad request\"}}\n"), nil)
	if err == nil || !strings.Contains(err.Error(), "bad request") {
		t.Fatalf("expected API error, got %v", err)
	}
}
