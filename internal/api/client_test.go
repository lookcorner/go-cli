package api

import (
	"encoding/json"
	"fmt"
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
			"type": "response.completed",
			"response": map[string]any{
				"id": "resp_1", "output": []any{toolItem},
				"usage": map[string]any{
					"input_tokens": 123, "output_tokens": 7, "total_tokens": 130,
					"cost_in_usd_ticks":     1_234_500_000,
					"input_tokens_details":  map[string]any{"cached_tokens": 100},
					"output_tokens_details": map[string]any{"reasoning_tokens": 5},
				},
			},
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
	if result.Usage.InputTokens != 123 || result.Usage.OutputTokens != 7 || result.Usage.TotalTokens != 130 || result.Usage.CachedReadTokens != 100 || result.Usage.ReasoningTokens != 5 || result.Usage.CostUSDTicks == nil || *result.Usage.CostUSDTicks != 1_234_500_000 {
		t.Fatalf("usage missing: %#v", result.Usage)
	}
	if len(result.ToolCalls) != 1 {
		t.Fatalf("expected deduplicated tool call, got %#v", result.ToolCalls)
	}
	if result.ToolCalls[0].CallID != "call_1" || result.ToolCalls[0].Name != "read_file" {
		t.Fatalf("unexpected tool call: %#v", result.ToolCalls[0])
	}
}

func TestParseSSEReturnsReasoningSummaryEvents(t *testing.T) {
	stream := strings.Join([]string{
		sseLine(t, map[string]any{"type": "response.reasoning_summary_text.delta", "delta": "check "}),
		sseLine(t, map[string]any{"type": "response.reasoning_summary_text.delta", "delta": "inputs"}),
		sseLine(t, map[string]any{"type": "response.output_text.delta", "delta": "done"}),
	}, "\n")
	var events []StreamEvent
	result, err := parseSSEEvents(strings.NewReader(stream), func(event StreamEvent) { events = append(events, event) })
	if err != nil {
		t.Fatal(err)
	}
	if result.Thought != "check inputs" || result.Text != "done" || len(events) != 3 ||
		events[0] != (StreamEvent{Kind: StreamThought, Text: "check "}) ||
		events[2] != (StreamEvent{Kind: StreamText, Text: "done"}) {
		t.Fatalf("result=%#v events=%#v", result, events)
	}
}

func TestParseJSONReturnsReasoningSummary(t *testing.T) {
	body := `{"id":"resp_1","output":[{"type":"reasoning","summary":[{"type":"summary_text","text":"checked"}]},{"type":"message","content":[{"type":"output_text","text":"done"}]}]}`
	result, err := parseJSONEvents(strings.NewReader(body), nil)
	if err != nil || result.Thought != "checked" || result.Text != "done" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestParseJSONUsageDetails(t *testing.T) {
	result, err := parseJSON(strings.NewReader(`{"id":"resp_1","output":[],"usage":{"input_tokens":20,"output_tokens":4,"total_tokens":24,"cost_in_usd_ticks":99,"input_tokens_details":{"cached_tokens":12},"output_tokens_details":{"reasoning_tokens":3}}}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Usage.InputTokens != 20 || result.Usage.OutputTokens != 4 || result.Usage.TotalTokens != 24 || result.Usage.CachedReadTokens != 12 || result.Usage.ReasoningTokens != 3 || result.Usage.CostUSDTicks == nil || *result.Usage.CostUSDTicks != 99 {
		t.Fatalf("usage=%#v", result.Usage)
	}
}

func TestParseJSONIgnoresUnreportedCost(t *testing.T) {
	for _, cost := range []int64{0, -1} {
		result, err := parseJSON(strings.NewReader(fmt.Sprintf(`{"id":"resp_1","output":[],"usage":{"cost_in_usd_ticks":%d}}`, cost)), nil)
		if err != nil || result.Usage.CostUSDTicks != nil {
			t.Fatalf("cost=%d usage=%#v err=%v", cost, result.Usage, err)
		}
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
