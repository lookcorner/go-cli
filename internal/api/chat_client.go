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

	"github.com/lookcorner/go-cli/internal/session"
)

type ChatClient struct {
	baseURL       string
	apiKey        string
	tokenProvider TokenProvider
	http          *http.Client
	mu            sync.Mutex
	history       []chatMessage
	nextID        atomic.Uint64
	pruning       PruningConfig
}

func (c *ChatClient) SetTokenProvider(provider TokenProvider) { c.tokenProvider = provider }

type chatMessage struct {
	Role       string         `json:"role"`
	Content    any            `json:"content,omitempty"`
	ToolCalls  []chatToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type chatContentPart struct {
	Type     string        `json:"type"`
	Text     string        `json:"text,omitempty"`
	ImageURL *chatImageURL `json:"image_url,omitempty"`
}

type chatImageURL struct {
	URL string `json:"url"`
}

type chatToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function chatFunction `json:"function"`
}

type chatFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type chatToolDefinition struct {
	Type     string `json:"type"`
	Function struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Parameters  map[string]any `json:"parameters"`
		Strict      bool           `json:"strict,omitempty"`
	} `json:"function"`
}

func NewChatClient(baseURL, apiKey string, httpClient *http.Client) *ChatClient {
	return &ChatClient{baseURL: strings.TrimRight(baseURL, "/"), apiKey: apiKey, http: httpClient, pruning: DefaultPruningConfig()}
}

func (c *ChatClient) CloneForCompaction(includeHistory bool) Streamer {
	c.mu.Lock()
	defer c.mu.Unlock()
	clone := &ChatClient{
		baseURL: c.baseURL, apiKey: c.apiKey, tokenProvider: c.tokenProvider, http: c.http, pruning: c.pruning,
	}
	if includeHistory {
		clone.history = append([]chatMessage(nil), c.history...)
	}
	return clone
}

func (c *ChatClient) ResetHistory(summary string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.history = nil
	_ = summary
}

func (c *ChatClient) RewindHistory(messages []session.Message) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.history = make([]chatMessage, 0, len(messages))
	for _, message := range messages {
		c.history = append(c.history, chatMessage{Role: message.Role, Content: chatContent(sessionMessageContent(message))})
	}
}

func sessionMessageContent(message session.Message) []ContentPart {
	if len(message.Content) == 0 {
		return []ContentPart{{Type: "input_text", Text: message.Text}}
	}
	parts := make([]ContentPart, 0, len(message.Content))
	for _, content := range message.Content {
		switch content.Type {
		case "text":
			parts = append(parts, ContentPart{Type: "input_text", Text: content.Text})
		case "image":
			uri := content.URI
			if content.Data != "" {
				uri = "data:" + content.MimeType + ";base64," + content.Data
			}
			parts = append(parts, ContentPart{Type: "input_image", ImageURL: uri})
		}
	}
	return parts
}

func (c *ChatClient) SetPruning(config PruningConfig) { c.pruning = config }

func (c *ChatClient) StreamResponse(ctx context.Context, request ResponseRequest, onText func(string)) (StreamResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	history := append([]chatMessage(nil), c.history...)
	for _, item := range request.Input {
		switch item.Type {
		case "message":
			history = append(history, chatMessage{Role: item.Role, Content: chatContent(item.Content)})
		case "function_call_output":
			history = append(history, chatMessage{Role: "tool", Content: item.Output, ToolCallID: item.CallID})
		default:
			return StreamResult{}, fmt.Errorf("chat backend does not support input item type %q", item.Type)
		}
	}
	pruneChatHistory(history, c.pruning)
	messages := make([]chatMessage, 0, len(history)+1)
	if request.Instructions != "" {
		messages = append(messages, chatMessage{Role: "system", Content: request.Instructions})
	}
	messages = append(messages, history...)
	definitions := make([]chatToolDefinition, 0, len(request.Tools))
	for _, definition := range request.Tools {
		var converted chatToolDefinition
		converted.Type = "function"
		converted.Function.Name = definition.Name
		converted.Function.Description = definition.Description
		converted.Function.Parameters = definition.Parameters
		converted.Function.Strict = definition.Strict
		definitions = append(definitions, converted)
	}
	payload := map[string]any{
		"model": request.Model, "messages": messages, "stream": true,
		"stream_options": map[string]any{"include_usage": true},
	}
	if request.MaxOutputTokens > 0 {
		payload["max_completion_tokens"] = request.MaxOutputTokens
	}
	if request.Reasoning != nil {
		payload["reasoning_effort"] = request.Reasoning.Effort
	}
	if len(definitions) > 0 {
		payload["tools"] = definitions
		payload["tool_choice"] = "auto"
		payload["parallel_tool_calls"] = request.ParallelToolCalls
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return StreamResult{}, fmt.Errorf("encode chat request: %w", err)
	}
	resp, err := sendAuthenticated(ctx, c.http, c.apiKey, c.tokenProvider, func(token string) (*http.Request, error) {
		httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("build chat request: %w", err)
		}
		httpRequest.Header.Set("Authorization", "Bearer "+token)
		httpRequest.Header.Set("Content-Type", "application/json")
		httpRequest.Header.Set("Accept", "text/event-stream")
		httpRequest.Header.Set("User-Agent", "gork-go/0.1")
		return httpRequest, nil
	})
	if err != nil {
		return StreamResult{}, fmt.Errorf("send chat request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		limited, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		return StreamResult{}, fmt.Errorf("chat API returned %s: %s", resp.Status, strings.TrimSpace(string(limited)))
	}
	var result StreamResult
	if strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		result, err = parseChatSSE(resp.Body, onText)
	} else {
		result, err = parseChatJSON(resp.Body, onText)
	}
	if err != nil {
		return StreamResult{}, err
	}
	if result.ResponseID == "" {
		result.ResponseID = fmt.Sprintf("chat_%d", c.nextID.Add(1))
	}
	assistant := chatMessage{Role: "assistant", Content: result.Text}
	for _, call := range result.ToolCalls {
		assistant.ToolCalls = append(assistant.ToolCalls, chatToolCall{
			ID: call.CallID, Type: "function",
			Function: chatFunction{Name: call.Name, Arguments: string(call.Arguments)},
		})
	}
	history = append(history, assistant)
	c.history = history
	return result, nil
}

func pruneChatHistory(history []chatMessage, cfg PruningConfig) {
	turnsAfter := 0
	for index := len(history) - 1; index >= 0; index-- {
		message := &history[index]
		if message.Role == "user" {
			turnsAfter++
		}
		if message.Role == "tool" {
			if content, ok := message.Content.(string); ok {
				message.Content = pruneToolResult(content, turnsAfter, cfg)
			}
		}
	}
}

func chatContent(content any) any {
	parts, ok := content.([]ContentPart)
	if !ok {
		if text, ok := content.(string); ok {
			return text
		}
		encoded, _ := json.Marshal(content)
		return string(encoded)
	}
	converted := make([]chatContentPart, 0, len(parts))
	for _, part := range parts {
		switch part.Type {
		case "input_text":
			converted = append(converted, chatContentPart{Type: "text", Text: part.Text})
		case "input_image":
			converted = append(converted, chatContentPart{Type: "image_url", ImageURL: &chatImageURL{URL: part.ImageURL}})
		}
	}
	return converted
}

type chatChunk struct {
	ID    string `json:"id"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage,omitempty"`
	Choices []struct {
		Delta struct {
			Content   string `json:"content"`
			ToolCalls []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
	} `json:"choices"`
}

type chatCallBuilder struct {
	id        string
	name      string
	arguments strings.Builder
}

func parseChatSSE(reader io.Reader, onText func(string)) (StreamResult, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), 8<<20)
	result := StreamResult{}
	builders := make(map[int]*chatCallBuilder)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		var chunk chatChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return StreamResult{}, fmt.Errorf("decode chat SSE chunk: %w", err)
		}
		if chunk.ID != "" {
			result.ResponseID = chunk.ID
		}
		if chunk.Usage != nil {
			result.Usage = Usage{InputTokens: chunk.Usage.PromptTokens, OutputTokens: chunk.Usage.CompletionTokens, TotalTokens: chunk.Usage.TotalTokens}
		}
		for _, choice := range chunk.Choices {
			if choice.Delta.Content != "" {
				result.Text += choice.Delta.Content
				if onText != nil {
					onText(choice.Delta.Content)
				}
			}
			for _, call := range choice.Delta.ToolCalls {
				builder := builders[call.Index]
				if builder == nil {
					builder = &chatCallBuilder{}
					builders[call.Index] = builder
				}
				if call.ID != "" {
					builder.id = call.ID
				}
				if call.Function.Name != "" {
					builder.name = call.Function.Name
				}
				builder.arguments.WriteString(call.Function.Arguments)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return StreamResult{}, fmt.Errorf("read chat stream: %w", err)
	}
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

func parseChatJSON(reader io.Reader, onText func(string)) (StreamResult, error) {
	var response struct {
		ID    string `json:"id"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
		Choices []struct {
			Message chatMessage `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(reader).Decode(&response); err != nil {
		return StreamResult{}, fmt.Errorf("decode chat response: %w", err)
	}
	result := StreamResult{ResponseID: response.ID, Usage: Usage{
		InputTokens: response.Usage.PromptTokens, OutputTokens: response.Usage.CompletionTokens, TotalTokens: response.Usage.TotalTokens,
	}}
	for _, choice := range response.Choices {
		content, _ := choice.Message.Content.(string)
		result.Text += content
		if onText != nil && content != "" {
			onText(content)
		}
		for _, call := range choice.Message.ToolCalls {
			arguments := call.Function.Arguments
			if arguments == "" {
				arguments = "{}"
			}
			result.ToolCalls = append(result.ToolCalls, ToolCall{
				ID: call.ID, CallID: call.ID, Name: call.Function.Name, Arguments: json.RawMessage(arguments),
			})
		}
	}
	return result, nil
}
