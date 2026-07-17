package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

func NewClient(baseURL, apiKey string, httpClient *http.Client) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		http:    httpClient,
	}
}

func (c *Client) StreamResponse(ctx context.Context, request ResponseRequest, onText func(string)) (StreamResult, error) {
	body, err := json.Marshal(request)
	if err != nil {
		return StreamResult{}, fmt.Errorf("encode response request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/responses", bytes.NewReader(body))
	if err != nil {
		return StreamResult{}, fmt.Errorf("build response request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("User-Agent", "gork-go/0.1")

	resp, err := c.http.Do(req)
	if err != nil {
		return StreamResult{}, fmt.Errorf("send response request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		limited, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		return StreamResult{}, fmt.Errorf("responses API returned %s: %s", resp.Status, strings.TrimSpace(string(limited)))
	}

	contentType := resp.Header.Get("Content-Type")
	if strings.Contains(contentType, "text/event-stream") {
		return parseSSE(resp.Body, onText)
	}
	return parseJSON(resp.Body, onText)
}

type wireEvent struct {
	Type     string          `json:"type"`
	Delta    string          `json:"delta,omitempty"`
	Item     json.RawMessage `json:"item,omitempty"`
	Response json.RawMessage `json:"response,omitempty"`
	Error    *wireError      `json:"error,omitempty"`
}

type wireError struct {
	Message string `json:"message"`
}

type wireResponse struct {
	ID     string            `json:"id"`
	Output []json.RawMessage `json:"output"`
	Usage  struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
		TotalTokens  int `json:"total_tokens"`
	} `json:"usage"`
}

func parseSSE(reader io.Reader, onText func(string)) (StreamResult, error) {
	scanner := bufio.NewScanner(reader)
	buffer := make([]byte, 64<<10)
	scanner.Buffer(buffer, 8<<20)
	var result StreamResult
	seenCalls := make(map[string]struct{})

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		var event wireEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return StreamResult{}, fmt.Errorf("decode SSE event: %w", err)
		}
		if event.Type == "error" || event.Type == "response.failed" {
			message := "response stream failed"
			if event.Error != nil && event.Error.Message != "" {
				message = event.Error.Message
			}
			return StreamResult{}, errors.New(message)
		}
		if event.Type == "response.output_text.delta" && event.Delta != "" {
			result.Text += event.Delta
			if onText != nil {
				onText(event.Delta)
			}
		}
		if len(event.Item) > 0 && event.Type == "response.output_item.done" {
			appendToolCall(event.Item, &result, seenCalls)
		}
		if len(event.Response) > 0 && (event.Type == "response.completed" || event.Type == "response.incomplete") {
			var response wireResponse
			if err := json.Unmarshal(event.Response, &response); err != nil {
				return StreamResult{}, fmt.Errorf("decode terminal response: %w", err)
			}
			result.ResponseID = response.ID
			result.Usage = Usage{InputTokens: response.Usage.InputTokens, OutputTokens: response.Usage.OutputTokens, TotalTokens: response.Usage.TotalTokens}
			for _, item := range response.Output {
				appendToolCall(item, &result, seenCalls)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return StreamResult{}, fmt.Errorf("read response stream: %w", err)
	}
	return result, nil
}

func parseJSON(reader io.Reader, onText func(string)) (StreamResult, error) {
	var response wireResponse
	if err := json.NewDecoder(reader).Decode(&response); err != nil {
		return StreamResult{}, fmt.Errorf("decode response: %w", err)
	}
	result := StreamResult{ResponseID: response.ID, Usage: Usage{
		InputTokens: response.Usage.InputTokens, OutputTokens: response.Usage.OutputTokens, TotalTokens: response.Usage.TotalTokens,
	}}
	seenCalls := make(map[string]struct{})
	for _, raw := range response.Output {
		var item struct {
			Type    string `json:"type"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		if json.Unmarshal(raw, &item) == nil && item.Type == "message" {
			for _, content := range item.Content {
				if content.Type == "output_text" {
					result.Text += content.Text
					if onText != nil {
						onText(content.Text)
					}
				}
			}
		}
		appendToolCall(raw, &result, seenCalls)
	}
	return result, nil
}

func appendToolCall(raw json.RawMessage, result *StreamResult, seen map[string]struct{}) {
	var item struct {
		Type      string          `json:"type"`
		ID        string          `json:"id"`
		CallID    string          `json:"call_id"`
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(raw, &item); err != nil || item.Type != "function_call" || item.CallID == "" {
		return
	}
	if _, ok := seen[item.CallID]; ok {
		return
	}
	seen[item.CallID] = struct{}{}
	result.ToolCalls = append(result.ToolCalls, ToolCall{
		ID: item.ID, CallID: item.CallID, Name: item.Name, Arguments: item.Arguments,
	})
}
