package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

type MessagesClient struct {
	baseURL string
	apiKey  string
	http    *http.Client
	mu      sync.Mutex
	history []messagesMessage
	nextID  atomic.Uint64
	pruning PruningConfig
}

type messagesMessage struct {
	Role    string          `json:"role"`
	Content []messagesBlock `json:"content"`
}

type messagesBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
	Source    *messagesSource `json:"source,omitempty"`
}

type messagesSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type messagesTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema"`
}

func NewMessagesClient(baseURL, apiKey string, httpClient *http.Client) *MessagesClient {
	return &MessagesClient{baseURL: strings.TrimRight(baseURL, "/"), apiKey: apiKey, http: httpClient, pruning: DefaultPruningConfig()}
}

func (c *MessagesClient) ResetHistory(summary string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.history = nil
	_ = summary
}

func (c *MessagesClient) SetPruning(config PruningConfig) { c.pruning = config }

func (c *MessagesClient) StreamResponse(ctx context.Context, request ResponseRequest, onText func(string)) (StreamResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	history := append([]messagesMessage(nil), c.history...)
	for _, item := range request.Input {
		switch item.Type {
		case "message":
			blocks, err := messagesContent(item.Content)
			if err != nil {
				return StreamResult{}, err
			}
			if len(history) > 0 && history[len(history)-1].Role == item.Role {
				history[len(history)-1].Content = append(history[len(history)-1].Content, blocks...)
			} else {
				history = append(history, messagesMessage{Role: item.Role, Content: blocks})
			}
		case "function_call_output":
			block := messagesBlock{Type: "tool_result", ToolUseID: item.CallID, Content: item.Output}
			if len(history) > 0 && history[len(history)-1].Role == "user" {
				history[len(history)-1].Content = append(history[len(history)-1].Content, block)
			} else {
				history = append(history, messagesMessage{Role: "user", Content: []messagesBlock{block}})
			}
		default:
			return StreamResult{}, fmt.Errorf("messages backend does not support input item type %q", item.Type)
		}
	}
	pruneMessagesHistory(history, c.pruning)
	definitions := make([]messagesTool, 0, len(request.Tools))
	for _, definition := range request.Tools {
		definitions = append(definitions, messagesTool{
			Name: definition.Name, Description: definition.Description, InputSchema: definition.Parameters,
		})
	}
	payload := map[string]any{
		"model": request.Model, "max_tokens": 32768, "system": request.Instructions,
		"messages": history, "stream": true,
	}
	if len(definitions) > 0 {
		payload["tools"] = definitions
		payload["tool_choice"] = map[string]any{"type": "auto"}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return StreamResult{}, fmt.Errorf("encode messages request: %w", err)
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/messages", bytes.NewReader(body))
	if err != nil {
		return StreamResult{}, fmt.Errorf("build messages request: %w", err)
	}
	httpRequest.Header.Set("x-api-key", c.apiKey)
	httpRequest.Header.Set("anthropic-version", "2023-06-01")
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "text/event-stream")
	httpRequest.Header.Set("User-Agent", "gork-go/0.1")
	resp, err := c.http.Do(httpRequest)
	if err != nil {
		return StreamResult{}, fmt.Errorf("send messages request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		limited, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		return StreamResult{}, fmt.Errorf("messages API returned %s: %s", resp.Status, strings.TrimSpace(string(limited)))
	}
	var result StreamResult
	if strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		result, err = parseMessagesSSE(resp.Body, onText)
	} else {
		result, err = parseMessagesJSON(resp.Body, onText)
	}
	if err != nil {
		return StreamResult{}, err
	}
	if result.ResponseID == "" {
		result.ResponseID = fmt.Sprintf("msg_%d", c.nextID.Add(1))
	}
	assistant := messagesMessage{Role: "assistant"}
	if result.Text != "" {
		assistant.Content = append(assistant.Content, messagesBlock{Type: "text", Text: result.Text})
	}
	for _, call := range result.ToolCalls {
		assistant.Content = append(assistant.Content, messagesBlock{
			Type: "tool_use", ID: call.CallID, Name: call.Name, Input: call.Arguments,
		})
	}
	history = append(history, assistant)
	c.history = history
	return result, nil
}

func messagesContent(content any) ([]messagesBlock, error) {
	parts, ok := content.([]ContentPart)
	if !ok {
		text, ok := content.(string)
		if !ok {
			encoded, _ := json.Marshal(content)
			text = string(encoded)
		}
		return []messagesBlock{{Type: "text", Text: text}}, nil
	}
	blocks := make([]messagesBlock, 0, len(parts))
	for _, part := range parts {
		switch part.Type {
		case "input_text":
			blocks = append(blocks, messagesBlock{Type: "text", Text: part.Text})
		case "input_image":
			header, data, found := strings.Cut(part.ImageURL, ",")
			mediaType := strings.TrimSuffix(strings.TrimPrefix(header, "data:"), ";base64")
			if !found || !strings.HasSuffix(header, ";base64") || !strings.HasPrefix(mediaType, "image/") || data == "" {
				return nil, fmt.Errorf("messages backend received an invalid image data URL")
			}
			blocks = append(blocks, messagesBlock{Type: "image", Source: &messagesSource{Type: "base64", MediaType: mediaType, Data: data}})
		}
	}
	return blocks, nil
}

func pruneMessagesHistory(history []messagesMessage, cfg PruningConfig) {
	turnsAfter := 0
	for index := len(history) - 1; index >= 0; index-- {
		message := &history[index]
		if message.Role == "user" && !allToolResults(message.Content) {
			turnsAfter++
		}
		for blockIndex := range message.Content {
			if message.Content[blockIndex].Type == "tool_result" {
				message.Content[blockIndex].Content = pruneToolResult(message.Content[blockIndex].Content, turnsAfter, cfg)
			}
		}
	}
}

func allToolResults(blocks []messagesBlock) bool {
	if len(blocks) == 0 {
		return false
	}
	for _, block := range blocks {
		if block.Type != "tool_result" {
			return false
		}
	}
	return true
}

type messagesEvent struct {
	Type    string `json:"type"`
	Index   int    `json:"index,omitempty"`
	Message struct {
		ID    string `json:"id"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	} `json:"message,omitempty"`
	ContentBlock struct {
		Type  string          `json:"type"`
		ID    string          `json:"id,omitempty"`
		Name  string          `json:"name,omitempty"`
		Input json.RawMessage `json:"input,omitempty"`
		Text  string          `json:"text,omitempty"`
	} `json:"content_block,omitempty"`
	Delta struct {
		Type        string `json:"type"`
		Text        string `json:"text,omitempty"`
		PartialJSON string `json:"partial_json,omitempty"`
	} `json:"delta,omitempty"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage,omitempty"`
	Error *wireError `json:"error,omitempty"`
}

type messagesCallBuilder struct {
	id        string
	name      string
	arguments strings.Builder
}

func parseMessagesSSE(reader io.Reader, onText func(string)) (StreamResult, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), 8<<20)
	result := StreamResult{}
	builders := make(map[int]*messagesCallBuilder)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		var event messagesEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return StreamResult{}, fmt.Errorf("decode messages SSE event: %w", err)
		}
		if event.Type == "error" {
			message := "messages stream failed"
			if event.Error != nil && event.Error.Message != "" {
				message = event.Error.Message
			}
			return StreamResult{}, fmt.Errorf("%s", message)
		}
		switch event.Type {
		case "message_start":
			result.ResponseID = event.Message.ID
			result.Usage.InputTokens = event.Message.Usage.InputTokens
			result.Usage.OutputTokens = event.Message.Usage.OutputTokens
		case "message_delta":
			if event.Usage.InputTokens > 0 {
				result.Usage.InputTokens = event.Usage.InputTokens
			}
			if event.Usage.OutputTokens > 0 {
				result.Usage.OutputTokens = event.Usage.OutputTokens
			}
		case "content_block_start":
			if event.ContentBlock.Type == "text" && event.ContentBlock.Text != "" {
				result.Text += event.ContentBlock.Text
				if onText != nil {
					onText(event.ContentBlock.Text)
				}
			}
			if event.ContentBlock.Type == "tool_use" {
				builder := &messagesCallBuilder{id: event.ContentBlock.ID, name: event.ContentBlock.Name}
				if len(event.ContentBlock.Input) > 0 && string(event.ContentBlock.Input) != "{}" {
					builder.arguments.Write(event.ContentBlock.Input)
				}
				builders[event.Index] = builder
			}
		case "content_block_delta":
			if event.Delta.Type == "text_delta" && event.Delta.Text != "" {
				result.Text += event.Delta.Text
				if onText != nil {
					onText(event.Delta.Text)
				}
			}
			if event.Delta.Type == "input_json_delta" {
				builder := builders[event.Index]
				if builder != nil {
					builder.arguments.WriteString(event.Delta.PartialJSON)
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return StreamResult{}, fmt.Errorf("read messages stream: %w", err)
	}
	result.Usage.TotalTokens = result.Usage.InputTokens + result.Usage.OutputTokens
	indexes := make([]int, 0, len(builders))
	for index := range builders {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	for _, index := range indexes {
		builder := builders[index]
		arguments := builder.arguments.String()
		if arguments == "" {
			arguments = "{}"
		}
		result.ToolCalls = append(result.ToolCalls, ToolCall{
			ID: builder.id, CallID: builder.id, Name: builder.name, Arguments: json.RawMessage(arguments),
		})
	}
	return result, nil
}

func parseMessagesJSON(reader io.Reader, onText func(string)) (StreamResult, error) {
	var response struct {
		ID      string          `json:"id"`
		Content []messagesBlock `json:"content"`
		Usage   struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(reader).Decode(&response); err != nil {
		return StreamResult{}, fmt.Errorf("decode messages response: %w", err)
	}
	result := StreamResult{ResponseID: response.ID, Usage: Usage{
		InputTokens: response.Usage.InputTokens, OutputTokens: response.Usage.OutputTokens,
		TotalTokens: response.Usage.InputTokens + response.Usage.OutputTokens,
	}}
	for _, block := range response.Content {
		switch block.Type {
		case "text":
			result.Text += block.Text
			if onText != nil {
				onText(block.Text)
			}
		case "tool_use":
			arguments := block.Input
			if len(arguments) == 0 {
				arguments = json.RawMessage(`{}`)
			}
			result.ToolCalls = append(result.ToolCalls, ToolCall{
				ID: block.ID, CallID: block.ID, Name: block.Name, Arguments: arguments,
			})
		}
	}
	return result, nil
}
