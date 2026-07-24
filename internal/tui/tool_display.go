package tui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/lookcorner/go-cli/internal/api"
	"github.com/lookcorner/go-cli/internal/tools"
)

const (
	toolCompactLines = 20
	toolCompactRunes = 4_000
	toolExpandLimit  = 256
)

type toolStartedEvent struct{ call api.ToolCall }

type toolFinishedEvent struct {
	call   api.ToolCall
	result tools.ExecutionResult
	err    error
}

func (b *Bridge) ToolStarted(call api.ToolCall) {
	b.send(toolStartedEvent{call: call})
}

func (b *Bridge) ToolFinished(call api.ToolCall, result tools.ExecutionResult, err error) {
	b.send(toolFinishedEvent{call: call, result: result, err: err})
}

func (m *model) finishTool(event toolFinishedEvent) {
	compact, folded := renderToolBlock(event.call, event.result, event.err, true)
	full, _ := renderToolBlock(event.call, event.result, event.err, false)
	m.appendSystem(compact)
	if folded {
		m.toolExpand = append(m.toolExpand, full)
		if len(m.toolExpand) > toolExpandLimit {
			copy(m.toolExpand, m.toolExpand[len(m.toolExpand)-toolExpandLimit:])
			m.toolExpand = m.toolExpand[:toolExpandLimit]
		}
	}
	if m.minimal {
		m.minimalFlushTo = m.transcript.Len()
	}
	if event.err != nil {
		m.status = "tool failed: " + event.call.Name
	} else {
		m.status = "tool finished: " + event.call.Name
	}
}

func (m *model) expandLastTool() {
	if !m.minimal {
		m.appendSystem("/expand is only available in minimal mode (--minimal)")
		m.status = "expand unavailable"
		return
	}
	if len(m.toolExpand) == 0 {
		m.status = "nothing folded to expand"
		return
	}
	last := len(m.toolExpand) - 1
	block := m.toolExpand[last]
	m.toolExpand = m.toolExpand[:last]
	m.appendSystem(block)
	m.minimalFlushTo = m.transcript.Len()
	m.status = "tool output expanded"
}

func renderToolBlock(call api.ToolCall, result tools.ExecutionResult, toolErr error, compact bool) (string, bool) {
	title := "Tool"
	if toolErr != nil {
		title = "Tool failed"
	}
	var sections []string
	folded := false
	if args := strings.TrimSpace(string(call.Arguments)); args != "" && args != "{}" {
		if pretty, err := prettyJSON(args); err == nil {
			args = pretty
		}
		if compact {
			var cut bool
			args, cut = compactToolText(args)
			folded = folded || cut
		}
		sections = append(sections, "Arguments\n\n"+toolFence("json", args))
	}
	output := strings.TrimSpace(result.Output)
	if toolErr != nil {
		if output != "" {
			output += "\n\n"
		}
		output += "ERROR: " + toolErr.Error()
	}
	if output == "" {
		output = "(no text output)"
	}
	if compact {
		var cut bool
		output, cut = compactToolText(output)
		folded = folded || cut
	}
	sections = append(sections, "Result\n\n"+toolFence("text", output))
	if len(result.Images) > 0 {
		lines := make([]string, 0, len(result.Images))
		for _, image := range result.Images {
			lines = append(lines, fmt.Sprintf("- %s · %dx%d · %d bytes", image.MediaType, image.Width, image.Height, len(image.Data)))
		}
		sections = append(sections, "Images\n\n"+strings.Join(lines, "\n"))
	}
	return fmt.Sprintf("#### %s: `%s`\n\n%s", title, call.Name, strings.Join(sections, "\n\n")), folded
}

func prettyJSON(value string) (string, error) {
	var raw any
	decoder := json.NewDecoder(bytes.NewBufferString(value))
	decoder.UseNumber()
	if err := decoder.Decode(&raw); err != nil {
		return "", err
	}
	pretty, err := json.MarshalIndent(raw, "", "  ")
	return string(pretty), err
}

func compactToolText(value string) (string, bool) {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	lines := strings.Split(value, "\n")
	folded := false
	if len(lines) > toolCompactLines {
		lines = lines[:toolCompactLines]
		folded = true
	}
	value = strings.Join(lines, "\n")
	runes := []rune(value)
	if len(runes) > toolCompactRunes {
		value = string(runes[:toolCompactRunes])
		folded = true
	}
	if folded {
		value = strings.TrimRight(value, "\n") + "\n… output folded; use /expand or Ctrl-E in minimal mode"
	}
	return value, folded
}

func toolFence(language, value string) string {
	fence := "```"
	for strings.Contains(value, fence) {
		fence += "`"
	}
	if !utf8.ValidString(value) {
		value = strings.ToValidUTF8(value, "�")
	}
	return fence + language + "\n" + value + "\n" + fence
}
