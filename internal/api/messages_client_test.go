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
		map[string]any{"type": "message_start", "message": map[string]any{
			"id": "msg_1", "usage": map[string]any{"input_tokens": 55, "output_tokens": 0},
		}},
		map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}},
		map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": "working"}},
		map[string]any{
			"type": "content_block_start", "index": 1,
			"content_block": map[string]any{"type": "tool_use", "id": "tool_1", "name": "read_file", "input": map[string]any{}},
		},
		map[string]any{"type": "content_block_delta", "index": 1, "delta": map[string]any{"type": "input_json_delta", "partial_json": "{\"path\":"}},
		map[string]any{"type": "content_block_delta", "index": 1, "delta": map[string]any{"type": "input_json_delta", "partial_json": "\"README.md\"}"}},
		map[string]any{"type": "message_delta", "usage": map[string]any{"output_tokens": 8}},
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
	if result.Usage.InputTokens != 55 || result.Usage.TotalTokens != 63 {
		t.Fatalf("usage missing: %#v", result.Usage)
	}
	call := result.ToolCalls[0]
	if call.CallID != "tool_1" || call.Name != "read_file" || string(call.Arguments) != "{\"path\":\"README.md\"}" {
		t.Fatalf("unexpected tool call: %#v", call)
	}
}

func TestMessagesClientRejectsReasoningEffortOverride(t *testing.T) {
	client := NewMessagesClient("https://example.invalid/v1", "key", &http.Client{})
	_, err := client.StreamResponse(context.Background(), ResponseRequest{Model: "model", Reasoning: &ReasoningConfig{Effort: "high"}}, nil)
	if err == nil || !strings.Contains(err.Error(), "does not support reasoning effort") {
		t.Fatalf("err=%v", err)
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
	temperature := 0.3
	first, err := client.StreamResponse(context.Background(), ResponseRequest{
		Model: "model", Instructions: "system", Stream: true,
		Temperature: &temperature,
		Input:       []InputItem{{Type: "message", Role: "user", Content: "inspect"}},
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
	if requests[0]["temperature"] != 0.3 {
		t.Fatalf("temperature missing: %#v", requests[0])
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

func TestMessagesClientMapsImageContent(t *testing.T) {
	blocks, err := messagesContent([]ContentPart{
		{Type: "input_text", Text: "inspect"},
		{Type: "input_image", ImageURL: "data:image/png;base64,cG5n"},
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(blocks)
	if err != nil {
		t.Fatal(err)
	}
	want := `[{"type":"text","text":"inspect"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"cG5n"}}]`
	if string(encoded) != want {
		t.Fatalf("unexpected messages image content: %s", encoded)
	}
	remote, err := messagesContent([]ContentPart{{Type: "input_image", ImageURL: "https://example.com/image.png"}})
	if err != nil || len(remote) != 1 || remote[0].Source.Type != "url" || remote[0].Source.URL != "https://example.com/image.png" {
		t.Fatalf("remote image URL was not mapped: blocks=%#v err=%v", remote, err)
	}
	if _, err := messagesContent([]ContentPart{{Type: "input_image", ImageURL: "file:///tmp/image.png"}}); err == nil {
		t.Fatal("unsafe image URL was accepted")
	}
}

func TestMessagesClientCombinesToolResultAndImage(t *testing.T) {
	var request map[string]any
	httpClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			return nil, err
		}
		return &http.Response{
			StatusCode: http.StatusOK, Status: "200 OK",
			Header: http.Header{"Content-Type": []string{"application/json"}},
			Body:   io.NopCloser(strings.NewReader(`{"id":"msg_1","content":[{"type":"text","text":"ok"}]}`)), Request: r,
		}, nil
	})}
	client := NewMessagesClient("https://example.invalid/v1", "key", httpClient)
	_, err := client.StreamResponse(context.Background(), ResponseRequest{Model: "model", Input: []InputItem{
		{Type: "function_call_output", CallID: "tool_1", Output: "image metadata"},
		{Type: "message", Role: "user", Content: []ContentPart{
			{Type: "input_text", Text: "inspect"},
			{Type: "input_image", ImageURL: "data:image/png;base64,cG5n"},
		}},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	messages := request["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("tool result and image were split across user messages: %#v", messages)
	}
	content := messages[0].(map[string]any)["content"].([]any)
	if len(content) != 3 || content[0].(map[string]any)["type"] != "tool_result" || content[2].(map[string]any)["type"] != "image" {
		t.Fatalf("unexpected combined content: %#v", content)
	}
}
