package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lookcorner/go-cli/internal/api"
	"github.com/lookcorner/go-cli/internal/session"
)

type ContextCategory struct {
	Label  string `json:"label"`
	Tokens int    `json:"tokens"`
	Detail string `json:"detail,omitempty"`
}

type ContextSnapshot struct {
	Used                        int               `json:"used"`
	Total                       int               `json:"total"`
	SystemPromptTokens          int               `json:"systemPromptTokens"`
	ToolDefinitionsCount        int               `json:"toolDefinitionsCount"`
	ToolDefinitionsTokens       int               `json:"toolDefinitionsTokens"`
	CompactionCount             int               `json:"compactionCount"`
	TurnCount                   int               `json:"turnCount"`
	ToolCallCount               int               `json:"toolCallCount"`
	MessageCount                int               `json:"messageCount"`
	MessageTokens               int               `json:"messageTokens"`
	FreeTokens                  int               `json:"freeTokens"`
	UsagePct                    int               `json:"usagePct"`
	AutoCompactThresholdPercent int               `json:"autoCompactThresholdPercent"`
	UsageCategories             []ContextCategory `json:"usageCategories,omitempty"`
}

func (r *Runner) ContextSnapshot(used int) ContextSnapshot {
	total, threshold := 0, 85
	if r != nil {
		total = max(r.ContextWindow, 0)
		if r.CompactThresholdPercent > 0 {
			threshold = r.CompactThresholdPercent
		}
	}
	used = max(used, 0)
	result := ContextSnapshot{
		Used: used, Total: total, FreeTokens: max(total-used, 0),
		UsagePct: contextPercent(used, total), AutoCompactThresholdPercent: threshold,
	}
	if r == nil {
		return result
	}
	result.SystemPromptTokens = estimateContextTokens(r.Instructions)
	mcpTokens, mcpServerCount := 0, 0
	if r.Tools != nil {
		tools := r.Tools.SnapshotTools()
		result.ToolDefinitionsCount = len(tools)
		mcpServers := make(map[string]bool)
		for _, tool := range tools {
			tokens := estimateToolDefinitionTokens(tool.Definition())
			result.ToolDefinitionsTokens += tokens
			if marker, ok := tool.(interface{ MCPServerName() string }); ok && marker.MCPServerName() != "" {
				mcpTokens += tokens
				mcpServers[marker.MCPServerName()] = true
			}
		}
		mcpServerCount = len(mcpServers)
	}
	if messages, err := session.TranscriptOrEmpty(r.SessionPath); err == nil {
		result.MessageCount = len(messages)
		for _, message := range messages {
			result.MessageTokens += estimateContextTokens(message.Text)
			if message.Role == "user" {
				result.TurnCount++
			}
		}
	}
	if r.Skills != nil {
		if listing, count := r.Skills.ListingSnapshot(); count > 0 {
			result.UsageCategories = append(result.UsageCategories, ContextCategory{
				Label: "Skills", Tokens: estimateContextTokens(listing), Detail: countDetail(count, "skill"),
			})
		}
	}
	if mcpServerCount > 0 {
		result.UsageCategories = append(result.UsageCategories, ContextCategory{
			Label: "MCP servers", Tokens: mcpTokens, Detail: countDetail(mcpServerCount, "server"),
		})
	}
	return result
}

func estimateToolDefinitionTokens(definition api.ToolDefinition) int {
	parameters, _ := json.Marshal(definition.Parameters)
	return estimateContextTokens(definition.Name + definition.Description + string(parameters))
}

func (s ContextSnapshot) Markdown(model string) string {
	lines := []string{
		"# Context usage",
		"",
		fmt.Sprintf("%d / %d tokens (%d%%)", s.Used, s.Total, s.UsagePct),
	}
	if strings.TrimSpace(model) != "" {
		lines = append(lines, model)
	}
	lines = append(lines, "",
		fmt.Sprintf("- System prompt: %d tokens", s.SystemPromptTokens),
		fmt.Sprintf("- Messages: %d tokens", s.MessageTokens),
		fmt.Sprintf("- Free: %d tokens", s.FreeTokens),
		"",
		fmt.Sprintf("- Tool definitions: %d tokens · %s", s.ToolDefinitionsTokens, countDetail(s.ToolDefinitionsCount, "tool")),
	)
	for _, category := range s.UsageCategories {
		lines = append(lines, fmt.Sprintf("- %s: %d tokens · %s", category.Label, category.Tokens, category.Detail))
	}
	lines = append(lines, "", fmt.Sprintf("Auto-compact at %d%%", s.AutoCompactThresholdPercent))
	return strings.Join(lines, "\n")
}

func estimateContextTokens(text string) int { return len([]byte(text)) / 4 }

func contextPercent(used, total int) int {
	if total <= 0 {
		return 0
	}
	return used * 100 / total
}

func countDetail(count int, noun string) string {
	if count != 1 {
		noun += "s"
	}
	return fmt.Sprintf("%d %s", count, noun)
}
