package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/lookcorner/go-cli/internal/api"
)

type contentTool struct {
	definition api.ToolDefinition
	execute    func(context.Context, json.RawMessage) (string, error)
	serverName string
	read       func(context.Context, string) ([]ResourceContents, error)
}

func (t *contentTool) Definition() api.ToolDefinition { return t.definition }
func (t *contentTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	return t.execute(ctx, raw)
}

func (t *contentTool) MCPResourceReader() (string, bool) {
	return t.serverName, t.read != nil
}

func (t *contentTool) MCPServerName() string { return t.serverName }

func (t *contentTool) ReadMCPResource(ctx context.Context, uri string) ([]ResourceContents, error) {
	if t.read == nil {
		return nil, errors.New("tool does not read MCP resources")
	}
	return t.read(ctx, uri)
}

func NewResourceAdapters(client *Client, serverName string) []*contentTool {
	list := &contentTool{serverName: serverName, definition: api.ToolDefinition{
		Type: "function", Name: modelContentToolName("resource", serverName, "list"),
		Description: fmt.Sprintf("List resources exposed by MCP server %s.", serverName),
		Parameters:  emptyObjectSchema(),
	}}
	list.execute = func(ctx context.Context, _ json.RawMessage) (string, error) {
		resources, err := client.ListResources(ctx)
		if err != nil {
			return "", err
		}
		encoded, err := json.Marshal(resources)
		return string(encoded), err
	}
	read := &contentTool{serverName: serverName, definition: api.ToolDefinition{
		Type: "function", Name: modelContentToolName("resource", serverName, "read"),
		Description: fmt.Sprintf("Read a resource from MCP server %s by URI.", serverName),
		Parameters: objectInputSchema(map[string]any{
			"uri": map[string]any{"type": "string", "description": "Exact URI returned by the resource list tool."},
		}, "uri"),
	}}
	read.read = client.ReadResource
	read.execute = func(ctx context.Context, raw json.RawMessage) (string, error) {
		var args struct {
			URI string `json:"uri"`
		}
		if json.Unmarshal(raw, &args) != nil || args.URI == "" {
			return "", errors.New("uri is required")
		}
		contents, err := client.ReadResource(ctx, args.URI)
		if err != nil {
			return "", err
		}
		return renderResourceContents(contents), nil
	}
	return []*contentTool{list, read}
}

func NewPromptAdapters(client *Client, serverName string) []*contentTool {
	list := &contentTool{serverName: serverName, definition: api.ToolDefinition{
		Type: "function", Name: modelContentToolName("prompt", serverName, "list"),
		Description: fmt.Sprintf("List reusable prompts exposed by MCP server %s.", serverName),
		Parameters:  emptyObjectSchema(),
	}}
	list.execute = func(ctx context.Context, _ json.RawMessage) (string, error) {
		prompts, err := client.ListPrompts(ctx)
		if err != nil {
			return "", err
		}
		encoded, err := json.Marshal(prompts)
		return string(encoded), err
	}
	get := &contentTool{serverName: serverName, definition: api.ToolDefinition{
		Type: "function", Name: modelContentToolName("prompt", serverName, "get"),
		Description: fmt.Sprintf("Render a reusable prompt from MCP server %s.", serverName),
		Parameters: objectInputSchema(map[string]any{
			"name":      map[string]any{"type": "string"},
			"arguments": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}},
		}, "name"),
	}}
	get.execute = func(ctx context.Context, raw json.RawMessage) (string, error) {
		var args struct {
			Name      string            `json:"name"`
			Arguments map[string]string `json:"arguments"`
		}
		if json.Unmarshal(raw, &args) != nil || args.Name == "" {
			return "", errors.New("name is required")
		}
		description, messages, err := client.GetPrompt(ctx, args.Name, args.Arguments)
		if err != nil {
			return "", err
		}
		parts := make([]string, 0, len(messages)+1)
		if description != "" {
			parts = append(parts, description)
		}
		for _, message := range messages {
			content := message.Content.Text
			if content == "" {
				encoded, _ := json.Marshal(message.Content)
				content = string(encoded)
			}
			parts = append(parts, message.Role+": "+content)
		}
		return strings.Join(parts, "\n\n"), nil
	}
	return []*contentTool{list, get}
}

func renderResourceContents(contents []ResourceContents) string {
	parts := make([]string, 0, len(contents))
	for _, content := range contents {
		if content.Text != "" {
			parts = append(parts, fmt.Sprintf("%s (%s):\n%s", content.URI, content.MIMEType, content.Text))
		} else {
			encoded, _ := json.Marshal(content)
			parts = append(parts, string(encoded))
		}
	}
	return strings.Join(parts, "\n\n")
}

func modelContentToolName(kind, serverName, operation string) string {
	return modelToolName(kind, serverName+"__"+operation)
}

func emptyObjectSchema() map[string]any { return objectInputSchema(map[string]any{}) }

func objectInputSchema(properties map[string]any, required ...string) map[string]any {
	return map[string]any{
		"type": "object", "properties": properties,
		"required": required, "additionalProperties": false,
	}
}
