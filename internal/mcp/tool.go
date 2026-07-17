package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/lookcorner/go-cli/internal/api"
	"github.com/lookcorner/go-cli/internal/tools"
)

var invalidToolName = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

type ToolAdapter struct {
	client     *Client
	serverName string
	remoteName string
	definition api.ToolDefinition
	approver   tools.Approver
}

func NewToolAdapters(client *Client, serverName string, remoteTools []ToolInfo, approver tools.Approver) []*ToolAdapter {
	result := make([]*ToolAdapter, 0, len(remoteTools))
	for _, remote := range remoteTools {
		schema := remote.InputSchema
		if schema == nil {
			schema = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		result = append(result, &ToolAdapter{
			client: client, serverName: serverName, remoteName: remote.Name, approver: approver,
			definition: api.ToolDefinition{
				Type: "function", Name: modelToolName(serverName, remote.Name),
				Description: fmt.Sprintf("MCP server %s: %s", serverName, remote.Description),
				Parameters:  schema,
			},
		})
	}
	return result
}

func (t *ToolAdapter) Definition() api.ToolDefinition { return t.definition }

func (t *ToolAdapter) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var arguments map[string]any
	if len(raw) == 0 {
		arguments = map[string]any{}
	} else if err := json.Unmarshal(raw, &arguments); err != nil {
		return "", fmt.Errorf("decode MCP tool arguments: %w", err)
	}
	if t.approver != nil {
		detail := fmt.Sprintf("%s/%s %s", t.serverName, t.remoteName, compactJSON(arguments))
		if err := t.approver.Approve(ctx, "MCP tool", detail); err != nil {
			return "", err
		}
	}
	result, err := t.client.CallTool(ctx, t.remoteName, arguments)
	if err != nil {
		return "", err
	}
	var parts []string
	for _, content := range result.Content {
		switch content.Type {
		case "text":
			parts = append(parts, content.Text)
		default:
			encoded, _ := json.Marshal(content)
			parts = append(parts, string(encoded))
		}
	}
	if result.StructuredContent != nil {
		encoded, _ := json.Marshal(result.StructuredContent)
		parts = append(parts, string(encoded))
	}
	output := strings.Join(parts, "\n")
	if result.IsError {
		if output == "" {
			output = "MCP tool returned an error"
		}
		return output, errors.New(output)
	}
	if output == "" {
		return "MCP tool completed with no content", nil
	}
	return output, nil
}

func modelToolName(serverName, remoteName string) string {
	base := "mcp__" + sanitize(serverName) + "__" + sanitize(remoteName)
	if len(base) <= 64 {
		return base
	}
	sum := sha256.Sum256([]byte(base))
	suffix := "_" + hex.EncodeToString(sum[:4])
	return base[:64-len(suffix)] + suffix
}

func sanitize(value string) string {
	value = invalidToolName.ReplaceAllString(value, "_")
	value = strings.Trim(value, "_")
	if value == "" {
		return "tool"
	}
	return value
}

func compactJSON(value any) string {
	encoded, _ := json.Marshal(value)
	if len(encoded) > 500 {
		return string(encoded[:500]) + "..."
	}
	return string(encoded)
}
