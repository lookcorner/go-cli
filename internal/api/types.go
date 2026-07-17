package api

import "encoding/json"

type ToolDefinition struct {
	Type        string         `json:"type"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
	Strict      bool           `json:"strict,omitempty"`
}

type InputItem struct {
	Type    string `json:"type"`
	Role    string `json:"role,omitempty"`
	Content any    `json:"content,omitempty"`
	CallID  string `json:"call_id,omitempty"`
	Output  string `json:"output,omitempty"`
}

type ResponseRequest struct {
	Model              string           `json:"model"`
	Instructions       string           `json:"instructions,omitempty"`
	Input              []InputItem      `json:"input"`
	Tools              []ToolDefinition `json:"tools,omitempty"`
	ToolChoice         string           `json:"tool_choice,omitempty"`
	ParallelToolCalls  bool             `json:"parallel_tool_calls"`
	PreviousResponseID string           `json:"previous_response_id,omitempty"`
	Stream             bool             `json:"stream"`
}

type ToolCall struct {
	ID        string          `json:"id,omitempty"`
	CallID    string          `json:"call_id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type StreamResult struct {
	ResponseID string
	Text       string
	ToolCalls  []ToolCall
}
