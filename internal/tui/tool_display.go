package tui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/lookcorner/go-cli/internal/api"
	"github.com/lookcorner/go-cli/internal/session"
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
	images := make([]session.DisplayImage, 0, len(result.Images))
	for _, image := range result.Images {
		images = append(images, session.DisplayImage{
			MediaType: image.MediaType, Width: image.Width, Height: image.Height, Bytes: len(image.Data),
		})
	}
	output := result.Output
	if toolErr != nil {
		if strings.TrimSpace(output) != "" {
			output += "\n\n"
		}
		output += "ERROR: " + toolErr.Error()
	}
	return renderStoredToolBlock(session.DisplayTool{
		Name: call.Name, Arguments: call.Arguments, Output: output, Failed: toolErr != nil,
		ImageCount: len(images), Images: images,
	}, compact)
}

func renderStoredToolBlock(tool session.DisplayTool, compact bool) (string, bool) {
	title := "Tool"
	if tool.Failed {
		title = "Tool failed"
	}
	var sections []string
	folded := false
	if args := strings.TrimSpace(string(tool.Arguments)); args != "" && args != "{}" {
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
	output := strings.TrimSpace(tool.Output)
	if output == "" {
		output = "(no text output)"
	}
	if compact {
		var cut bool
		output, cut = compactToolText(output)
		folded = folded || cut
	}
	sections = append(sections, "Result\n\n"+toolFence("text", output))
	if len(tool.Images) > 0 {
		lines := make([]string, 0, len(tool.Images))
		for _, image := range tool.Images {
			lines = append(lines, fmt.Sprintf("- %s · %dx%d · %d bytes", image.MediaType, image.Width, image.Height, image.Bytes))
		}
		sections = append(sections, "Images\n\n"+strings.Join(lines, "\n"))
	} else if tool.ImageCount > 0 {
		sections = append(sections, fmt.Sprintf("Images\n\n- %d image attachment(s)", tool.ImageCount))
	}
	return fmt.Sprintf("#### %s: `%s`\n\n%s", title, tool.Name, strings.Join(sections, "\n\n")), folded
}

func sessionDisplayTranscript(path string) (string, []transcriptMessage, []string, error) {
	entries, err := session.DisplayTimeline(path)
	if err != nil {
		return "", nil, nil, err
	}
	var text strings.Builder
	var messages []transcriptMessage
	var expands []string
	assistantOpen := false
	lastKind := ""
	separate := func() {
		if text.Len() > 0 {
			text.WriteString("\n\n")
		}
	}
	label := func(value, role string, at session.DisplayEntry) {
		start := text.Len()
		text.WriteString(value)
		messages = append(messages, transcriptMessage{start: start, offset: text.Len(), at: at.Time, role: role})
		text.WriteByte('\n')
	}
	for _, entry := range entries {
		switch entry.Kind {
		case "user":
			if entry.Synthetic {
				assistantOpen = false
				lastKind = ""
				continue
			}
			separate()
			label("You", "user", entry)
			text.WriteString(displayPromptBody(entry))
			assistantOpen = false
			lastKind = "user"
		case "assistant", "tool":
			if !assistantOpen {
				separate()
				label("Gork", "assistant", entry)
				assistantOpen = true
				lastKind = ""
			}
			if lastKind == "tool" || entry.Kind == "tool" && lastKind != "" {
				text.WriteString("\n\n")
			}
			if entry.Kind == "assistant" {
				text.WriteString(entry.Text)
			} else if entry.Tool != nil {
				compact, folded := renderStoredToolBlock(*entry.Tool, true)
				text.WriteString(compact)
				if folded {
					full, _ := renderStoredToolBlock(*entry.Tool, false)
					expands = append(expands, full)
					if len(expands) > toolExpandLimit {
						expands = expands[len(expands)-toolExpandLimit:]
					}
				}
			}
			lastKind = entry.Kind
		}
	}
	return strings.TrimSpace(text.String()), messages, expands, nil
}

func displayPromptBody(entry session.DisplayEntry) string {
	if len(entry.Content) == 0 {
		return entry.Text
	}
	content := make([]string, 0, len(entry.Content))
	for _, part := range entry.Content {
		switch part.Type {
		case "text":
			content = append(content, part.Text)
		case "image":
			if strings.HasPrefix(part.URI, "http://") || strings.HasPrefix(part.URI, "https://") {
				content = append(content, "[Image: "+part.URI+"]")
			} else {
				content = append(content, "[Image]")
			}
		}
	}
	return strings.Join(content, "\n")
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
