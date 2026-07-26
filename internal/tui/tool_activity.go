package tui

import (
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/lookcorner/go-cli/internal/api"
)

const toolActivityTitleMax = 30

// toolActivityLabel builds the terminal-title activity string for an in-flight tool.
// Prefer a trimmed description subject ("{desc}…"), else "Running: {command|name}",
// else "Running tool". Titles truncate at 30 runes.
func toolActivityLabel(call api.ToolCall) string {
	fields := parseToolActivityFields(call.Arguments)
	if desc := strings.TrimSpace(fields.description); desc != "" {
		return clampActivitySubject(desc) + "…"
	}
	title := strings.TrimSpace(fields.command)
	if title == "" {
		title = strings.TrimSpace(call.Name)
	}
	if title == "" {
		return "Running tool"
	}
	return "Running: " + truncateRunes(firstLine(title), toolActivityTitleMax)
}

type toolActivityInput struct {
	description string
	command     string
}

func parseToolActivityFields(arguments json.RawMessage) toolActivityInput {
	if len(arguments) == 0 {
		return toolActivityInput{}
	}
	var payload map[string]any
	if json.Unmarshal(arguments, &payload) != nil {
		return toolActivityInput{}
	}
	return toolActivityInput{
		description: stringField(payload, "description"),
		command:     stringField(payload, "command"),
	}
}

func stringField(payload map[string]any, key string) string {
	value, _ := payload[key].(string)
	return value
}

func clampActivitySubject(subject string) string {
	return truncateRunes(firstLine(strings.TrimSpace(subject)), toolActivityTitleMax)
}

func firstLine(value string) string {
	if index := strings.IndexAny(value, "\r\n"); index >= 0 {
		return value[:index]
	}
	return value
}

func truncateRunes(value string, max int) string {
	if max <= 0 || value == "" {
		return ""
	}
	if utf8.RuneCountInString(value) <= max {
		return value
	}
	runes := []rune(value)
	return string(runes[:max]) + "…"
}
