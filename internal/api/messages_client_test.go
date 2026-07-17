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

func TestParseMessagesSSE(t *testing.T) {
	events := []any{
		map[string]any{"type": "message_start", "message": map[string]any{"id": "msg_1"}},
		map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}},
		map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": "working"}},
		map[string]any{
			"type": "content_block_start", "index": 1,
			"content_block": map[string]any{"type": "tool_use", "id": "tool_1", "name": "read_file", "input": map[string]any{}},
		},
		map[string]any{"type": "content_block_delta", "index": 1, "delta": map[string]any{"type": "input_json_delta", "partial_json": "{\"path\":"}},
		map[string]any{"type": "content_block_delta", "index": 1, "delta": map[string]any{"type": "input_json_delta", "partial_json": "\"README.md\"}"}},
		map[string]any{"type": "message_stop"},
	}
	var lines []string
	for _, event := range events {
		lines = append(lines, sseLine(t, event), "")
	}
	result, err := parseMessagesSSE(strings.NewReader(strings.Join(lines, "\n")), nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.ResponseID != "msg_1" || result.Text != "working" || len(result.ToolCalls) != 1 {
		t.Fatalf("unexpected result: %#v", result)
	}
	call := result.ToolCalls[0]
	if call.CallID != "tool_1" || call.Name != "read_file" || string(call.Arguments) != "{\"path\":\"README.md\"}" {
		t.Fatalf("unexpected tool call: %#v", call)
	}
}

func TestMessagesClientCarriesToolHistory(t *testing.T) {
	var requests []map[string]any
	httpClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("x-api-key") != "key" || r.Header.Get("anthropic-version") == "" {
			return nil, fmt.Errorf("missing Anthropic headers")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			return nil, err
		}
		requests = append(requests, body)
		var events []any
		if len(requests) == 1 {
			events = []any{
				map[string]any{"type": "message_start", "message": map[string]any{"id": "msg_1"}},
				map[string]any{
					"type": "content_block_start", "index": 0,
					"content_block": map[string]any{
						"type": "tool_use", "id": "tool_1", "name": "read_file",
						"input": map[string]any{"path": "README.md"},
					},
				},
			}
		} else {
			events = []any{
				map[string]any{"type": "message_start", "message": map[string]any{"id": "msg_2"}},
				map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": "done"}},
			}
		}
		var lines []string
		for _, event := range events {
			lines = append(lines, sseLine(t, event), "")
		}
		return &http.Response{
			StatusCode: http.StatusOK, Status: "200 OK",
			Header: http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:   io.NopCloser(strings.NewReader(strings.Join(lines, "\n"))), Request: r,
		}, nil
	})}

	client := NewMessagesClient("https://example.invalid/v1", "key", httpClient)
	first, err := client.StreamResponse(context.Background(), ResponseRequest{
		Model: "model", Instructions: "system", Stream: true,
		Input: []InputItem{{Type: "message", Role: "user", Content: "inspect"}},
	}, nil)
	if err != nil || len(first.ToolCalls) != 1 {
		t.Fatalf("first response=%#v err=%v", first, err)
	}
	second, err := client.StreamResponse(context.Background(), ResponseRequest{
		Model: "model", Instructions: "system", Stream: true,
		Input: []InputItem{{Type: "function_call_output", CallID: "tool_1", Output: "file contents"}},
	}, nil)
	if err != nil || second.Text != "done" {
		t.Fatalf("second response=%#v err=%v", second, err)
	}
	if len(requests) != 2 {
		t.Fatalf("expected two requests, got %d", len(requests))
	}
	messages := requests[1]["messages"].([]any)
	if len(messages) != 3 {
		t.Fatalf("unexpected history: %#v", messages)
	}
	roles := []string{
		messages[0].(map[string]any)["role"].(string),
		messages[1].(map[string]any)["role"].(string),
		messages[2].(map[string]any)["role"].(string),
	}
	if strings.Join(roles, ",") != "user,assistant,user" {
		t.Fatalf("unexpected roles: %#v", roles)
	}
}
