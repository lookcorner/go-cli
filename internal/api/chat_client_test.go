package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestParseChatSSEIncrementalToolCall(t *testing.T) {
	firstCallDelta := map[string]any{
		"index": 0, "id": "call_1",
		"function": map[string]any{"name": "read_file", "arguments": "{\"pa"},
	}
	secondCallDelta := map[string]any{
		"index":    0,
		"function": map[string]any{"arguments": "th\":\"README.md\"}"},
	}
	stream := strings.Join([]string{
		sseLine(t, map[string]any{
			"id": "chat_1", "choices": []any{map[string]any{"delta": map[string]any{"content": "checking "}}},
		}),
		"",
		sseLine(t, map[string]any{
			"id": "chat_1", "choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{firstCallDelta}}}},
		}),
		"",
		sseLine(t, map[string]any{
			"id": "chat_1", "choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{secondCallDelta}}}},
		}),
		"",
		sseLine(t, map[string]any{
			"id": "chat_1", "choices": []any{},
			"usage": map[string]any{
				"prompt_tokens": 44, "completion_tokens": 6, "total_tokens": 50,
				"prompt_tokens_details":     map[string]any{"cached_tokens": 30},
				"completion_tokens_details": map[string]any{"reasoning_tokens": 4},
			},
		}),
		"",
		"data: [DONE]",
	}, "\n")
	result, err := parseChatSSE(strings.NewReader(stream), nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.ResponseID != "chat_1" || result.Text != "checking " || len(result.ToolCalls) != 1 {
		t.Fatalf("unexpected result: %#v", result)
	}
	if result.Usage.InputTokens != 44 || result.Usage.OutputTokens != 6 || result.Usage.TotalTokens != 50 || result.Usage.CachedReadTokens != 30 || result.Usage.ReasoningTokens != 4 {
		t.Fatalf("usage missing: %#v", result.Usage)
	}
	call := result.ToolCalls[0]
	if call.CallID != "call_1" || call.Name != "read_file" || string(call.Arguments) != "{\"path\":\"README.md\"}" {
		t.Fatalf("unexpected call: %#v", call)
	}
}

func TestParseChatJSONUsageDetails(t *testing.T) {
	result, err := parseChatJSON(strings.NewReader(`{"id":"chat_1","choices":[],"usage":{"prompt_tokens":20,"completion_tokens":4,"total_tokens":24,"prompt_tokens_details":{"cached_tokens":12},"completion_tokens_details":{"reasoning_tokens":3}}}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Usage != (Usage{InputTokens: 20, OutputTokens: 4, TotalTokens: 24, CachedReadTokens: 12, ReasoningTokens: 3}) {
		t.Fatalf("usage=%#v", result.Usage)
	}
}

func TestChatClientCarriesToolHistory(t *testing.T) {
	var requests []map[string]any
	httpClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			return nil, err
		}
		requests = append(requests, body)
		var event map[string]any
		if len(requests) == 1 {
			event = map[string]any{
				"id": "chat_1",
				"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{map[string]any{
					"index": 0, "id": "call_1",
					"function": map[string]any{"name": "read_file", "arguments": "{\"path\":\"README.md\"}"},
				}}}}},
			}
		} else {
			event = map[string]any{
				"id": "chat_2", "choices": []any{map[string]any{"delta": map[string]any{"content": "done"}}},
			}
		}
		stream := fmt.Sprintf("%s\n\ndata: [DONE]\n\n", sseLine(t, event))
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(stream)),
			Request:    r,
		}, nil
	})}

	client := NewChatClient("https://example.invalid/v1", "key", httpClient)
	temperature := 0.3
	first, err := client.StreamResponse(context.Background(), ResponseRequest{
		Model: "model", Instructions: "system", Stream: true, Reasoning: &ReasoningConfig{Effort: "high"},
		Temperature: &temperature,
		Input:       []InputItem{{Type: "message", Role: "user", Content: "inspect"}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.ToolCalls) != 1 {
		t.Fatalf("missing tool call: %#v", first)
	}
	second, err := client.StreamResponse(context.Background(), ResponseRequest{
		Model: "model", Instructions: "system", Stream: true, PreviousResponseID: first.ResponseID,
		Input: []InputItem{{Type: "function_call_output", CallID: "call_1", Output: "file contents"}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if second.Text != "done" {
		t.Fatalf("unexpected second response: %#v", second)
	}
	if len(requests) != 2 {
		t.Fatalf("expected two requests, got %d", len(requests))
	}
	if requests[0]["reasoning_effort"] != "high" {
		t.Fatalf("reasoning effort missing: %#v", requests[0])
	}
	if requests[0]["temperature"] != 0.3 {
		t.Fatalf("temperature missing: %#v", requests[0])
	}
	messages, ok := requests[1]["messages"].([]any)
	if !ok || len(messages) != 4 {
		t.Fatalf("unexpected second messages: %#v", requests[1]["messages"])
	}
	roles := make([]string, 0, len(messages))
	for _, raw := range messages {
		message := raw.(map[string]any)
		roles = append(roles, message["role"].(string))
	}
	if strings.Join(roles, ",") != "system,user,assistant,tool" {
		t.Fatalf("unexpected roles: %#v", roles)
	}
}

func TestChatClientMapsImageContent(t *testing.T) {
	content := chatContent([]ContentPart{
		{Type: "input_text", Text: "inspect"},
		{Type: "input_image", ImageURL: "data:image/png;base64,cG5n"},
	})
	encoded, err := json.Marshal(content)
	if err != nil {
		t.Fatal(err)
	}
	want := `[{"type":"text","text":"inspect"},{"type":"image_url","image_url":{"url":"data:image/png;base64,cG5n"}}]`
	if string(encoded) != want {
		t.Fatalf("unexpected chat image content: %s", encoded)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}
