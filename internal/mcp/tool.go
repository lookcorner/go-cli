package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/lookcorner/go-cli/internal/api"
	"github.com/lookcorner/go-cli/internal/tools"
)

var invalidToolName = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

var _ tools.ResultTool = (*ToolAdapter)(nil)

type ToolAdapter struct {
	client     *Client
	serverName string
	remoteName string
	remoteInfo ToolInfo
	definition api.ToolDefinition
	approver   tools.Approver
	output     OutputConfig
}

// OutputConfig keeps oversized MCP results recoverable without placing the
// complete payload in model context.
type OutputConfig struct {
	MaxBytes          uint64
	ArtifactDir       string
	ExposeImageBase64 bool
}

func NewToolAdapters(client *Client, serverName string, remoteTools []ToolInfo, approver tools.Approver, output ...OutputConfig) []*ToolAdapter {
	policy := OutputConfig{MaxBytes: 20_000}
	if len(output) > 0 {
		policy = output[0]
		if policy.MaxBytes == 0 {
			policy.MaxBytes = 20_000
		}
	}
	result := make([]*ToolAdapter, 0, len(remoteTools))
	for _, remote := range remoteTools {
		schema := remote.InputSchema
		if schema == nil {
			schema = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		result = append(result, &ToolAdapter{
			client: client, serverName: serverName, remoteName: remote.Name, remoteInfo: remote, approver: approver, output: policy,
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

func (t *ToolAdapter) MCPIdentity() (string, string, ToolInfo) {
	return t.serverName, t.remoteName, t.remoteInfo
}

func (t *ToolAdapter) MCPServerName() string { return t.serverName }

func (t *ToolAdapter) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	result, err := t.ExecuteResult(ctx, raw)
	return result.Output, err
}

func (t *ToolAdapter) ExecuteResult(ctx context.Context, raw json.RawMessage) (tools.ExecutionResult, error) {
	result, err := t.CallMCP(ctx, raw)
	if err != nil {
		return tools.ExecutionResult{}, err
	}
	var parts []string
	var images []tools.ImageAttachment
	for _, content := range result.Content {
		switch content.Type {
		case "text":
			parts = append(parts, content.Text)
		case "image":
			image, err := tools.DecodeImageAttachment(content.MIMEType, content.Data)
			if err != nil {
				return tools.ExecutionResult{}, fmt.Errorf("decode MCP image result: %w", err)
			}
			images = append(images, image)
			parts = append(parts, fmt.Sprintf("[Image: %s, %dx%d]", image.MediaType, image.Width, image.Height))
			if t.output.ExposeImageBase64 {
				parts = append(parts, fmt.Sprintf("<mcp_image_base64 mime=\"%s\">\n%s\n</mcp_image_base64>", content.MIMEType, content.Data))
			}
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
	output = t.boundOutput(ctx, output)
	if result.IsError {
		if output == "" {
			output = "MCP tool returned an error"
		}
		return tools.ExecutionResult{Output: output}, errors.New(output)
	}
	if output == "" {
		output = "MCP tool completed with no content"
	}
	return tools.ExecutionResult{Output: output, Images: images}, nil
}

func (t *ToolAdapter) boundOutput(ctx context.Context, output string) string {
	limit := int(min(t.output.MaxBytes, uint64(^uint(0)>>1)))
	if len(output) <= limit {
		return output
	}
	full := output
	preview := output[:limit]
	for !utf8.ValidString(preview) {
		preview = preview[:len(preview)-1]
	}
	hint := ""
	if path := t.writeFullOutput(ctx, full); path != "" {
		hint = " Full output written to: " + path + "."
		trimmed := strings.TrimSpace(full)
		longLine := longestLineBytes(full) > 2_000
		if json.Valid([]byte(trimmed)) {
			hint += " Use the shell with jq or Python to query the saved JSON."
		} else if longLine {
			hint += " Use the shell to slice or search the saved long-line text."
		}
	}
	return fmt.Sprintf("%s\n\n[MCP output truncated: showing first %d bytes of %d bytes.%s]", preview, limit, len(full), hint)
}

func (t *ToolAdapter) writeFullOutput(ctx context.Context, output string) string {
	artifactDir := tools.ToolArtifactDirFromContext(ctx)
	if artifactDir == "" {
		artifactDir = t.output.ArtifactDir
	}
	if artifactDir == "" {
		return ""
	}
	call, _ := tools.ToolCallFromContext(ctx)
	stem := sanitizeArtifactStem(call.ID)
	if stem == "" {
		stem = sanitizeArtifactStem(t.serverName + "-" + t.remoteName)
	}
	ext := ".txt"
	if json.Valid([]byte(strings.TrimSpace(output))) {
		ext = ".json"
	}
	dir := filepath.Join(artifactDir, "mcp")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return ""
	}
	path := filepath.Join(dir, stem+ext)
	if err := os.WriteFile(path, []byte(output), 0o600); err != nil {
		return ""
	}
	return path
}

func sanitizeArtifactStem(value string) string {
	var b strings.Builder
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return strings.Trim(b.String(), "_")
}

func longestLineBytes(value string) int {
	longest := 0
	for _, line := range strings.Split(value, "\n") {
		longest = max(longest, len(line))
	}
	return longest
}

func (t *ToolAdapter) CallMCP(ctx context.Context, raw json.RawMessage) (ToolResult, error) {
	var arguments map[string]any
	if len(raw) == 0 {
		arguments = map[string]any{}
	} else if err := json.Unmarshal(raw, &arguments); err != nil {
		return ToolResult{}, fmt.Errorf("decode MCP tool arguments: %w", err)
	}
	if t.approver != nil {
		detail := fmt.Sprintf("%s/%s %s", t.serverName, t.remoteName, compactJSON(arguments))
		if err := t.approver.Approve(ctx, "MCP tool", detail); err != nil {
			return ToolResult{}, err
		}
	}
	result, err := t.client.CallTool(ctx, t.remoteName, arguments)
	if err != nil {
		return ToolResult{}, err
	}
	return result, nil
}

func ModelToolName(serverName, remoteName string) string {
	return modelToolName(serverName, remoteName)
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
