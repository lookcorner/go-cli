package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lookcorner/go-cli/internal/api"
	"github.com/lookcorner/go-cli/internal/session"
	"github.com/lookcorner/go-cli/internal/tools"
)

func TestRenderToolBlockFoldsAndPreservesFullOutput(t *testing.T) {
	lines := make([]string, toolCompactLines+2)
	for index := range lines {
		lines[index] = fmt.Sprintf("line %02d", index+1)
	}
	call := api.ToolCall{CallID: "call-1", Name: "shell", Arguments: json.RawMessage("{\"command\":\"printf '```'\",\"timeout\":30}")}
	result := tools.ExecutionResult{
		Output: strings.Join(lines, "\n"),
		Images: []tools.ImageAttachment{{MediaType: "image/png", Width: 10, Height: 20, Data: []byte("png")}},
	}
	compact, folded := renderToolBlock(call, result, nil, true)
	full, fullFolded := renderToolBlock(call, result, nil, false)
	if !folded || fullFolded {
		t.Fatalf("folded flags: compact=%v full=%v", folded, fullFolded)
	}
	if !strings.Contains(compact, "output folded") || strings.Contains(compact, "line 22") {
		t.Fatalf("compact block was not folded:\n%s", compact)
	}
	if !strings.Contains(full, "line 22") || !strings.Contains(full, "image/png · 10x20 · 3 bytes") {
		t.Fatalf("full block lost output or image metadata:\n%s", full)
	}
	if strings.Count(full, "````") < 2 {
		t.Fatalf("embedded backticks did not widen the Markdown fence:\n%s", full)
	}
}

func TestMarkdownKeepsShorterBacktickRunInsideWideFence(t *testing.T) {
	rendered := strings.Join(renderMarkdown("````text\nbefore\n```\nafter\n````", 80), "\n")
	plain := stripMarkdownANSI(rendered)
	if strings.Contains(plain, "`text") || !strings.Contains(plain, "```") || !strings.Contains(plain, "after") {
		t.Fatalf("wide fence rendered incorrectly: %q", plain)
	}
}

func TestPrettyJSONPreservesLargeInteger(t *testing.T) {
	const value = `{"id":9007199254740993}`
	pretty, err := prettyJSON(value)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(pretty, "9007199254740993") {
		t.Fatalf("large integer changed across display formatting: %s", pretty)
	}
}

func TestToolResultCanBeExpandedInMinimalMode(t *testing.T) {
	m := &model{minimal: true, width: 80, height: 20}
	output := strings.Repeat("result line\n", toolCompactLines+1)
	m.finishTool(toolFinishedEvent{
		call:   api.ToolCall{Name: "read_file", Arguments: json.RawMessage(`{"path":"README.md"}`)},
		result: tools.ExecutionResult{Output: output},
	})
	if len(m.toolExpand) != 1 || !strings.Contains(m.transcript.String(), "output folded") {
		t.Fatalf("folded result was not retained: ring=%d\n%s", len(m.toolExpand), m.transcript.String())
	}
	before := m.transcript.Len()
	m.expandLastTool()
	if len(m.toolExpand) != 0 || m.transcript.Len() <= before || strings.Count(m.transcript.String(), "result line") <= toolCompactLines {
		t.Fatalf("full result was not reprinted:\n%s", m.transcript.String())
	}
	if m.minimalFlushTo != m.transcript.Len() || m.status != "tool output expanded" {
		t.Fatalf("minimal expansion was not committed: flush=%d len=%d status=%q", m.minimalFlushTo, m.transcript.Len(), m.status)
	}
}

func TestExpandOutsideMinimalModeExplainsRestriction(t *testing.T) {
	m := &model{}
	m.expandLastTool()
	if !strings.Contains(m.transcript.String(), "only available in minimal mode") || m.status != "expand unavailable" {
		t.Fatalf("unexpected fullscreen result: status=%q transcript=%q", m.status, m.transcript.String())
	}
}

func TestExpandCommandAndShortcutAreMinimalOnly(t *testing.T) {
	fullscreen := &model{width: 80, height: 20}
	fullscreen.setInput("/exp")
	for _, suggestion := range fullscreen.slashSuggestions() {
		if suggestion.insert == "/expand" {
			t.Fatalf("fullscreen exposed expand suggestion: %#v", suggestion)
		}
	}

	minimal := &model{minimal: true, width: 80, height: 20, toolExpand: []string{"#### Tool: `shell`\n\nfull output"}}
	minimal.setInput("/exp")
	suggestions := minimal.slashSuggestions()
	found := false
	for _, suggestion := range suggestions {
		found = found || suggestion.insert == "/expand"
	}
	if !found {
		t.Fatalf("minimal expand suggestion = %#v", suggestions)
	}
	minimal.clearInput()
	updated, command := minimal.handleKey(tea.KeyPressMsg(tea.Key{Code: 'e', Mod: tea.ModCtrl}))
	minimal = updated.(*model)
	if command != nil || len(minimal.toolExpand) != 0 || !strings.Contains(minimal.transcript.String(), "full output") {
		t.Fatalf("Ctrl-E did not expand: command=%v ring=%d transcript=%q", command != nil, len(minimal.toolExpand), minimal.transcript.String())
	}
}

func TestBridgePublishesToolLifecycle(t *testing.T) {
	bridge := NewBridge(context.Background(), tools.PermissionAuto)
	defer bridge.Close()
	call := api.ToolCall{CallID: "call-1", Name: "shell"}
	bridge.ToolStarted(call)
	started, ok := (<-bridge.events).(toolStartedEvent)
	if !ok || started.call.CallID != call.CallID {
		t.Fatalf("started event = %#v", started)
	}
	toolErr := errors.New("exit status 1")
	bridge.ToolFinished(call, tools.ExecutionResult{Output: "failed"}, toolErr)
	finished, ok := (<-bridge.events).(toolFinishedEvent)
	if !ok || !errors.Is(finished.err, toolErr) || finished.result.Output != "failed" {
		t.Fatalf("finished event = %#v", finished)
	}
}

func TestSessionDisplayTranscriptRestoresToolsInOrder(t *testing.T) {
	logger, err := session.NewLoggerWithID(t.TempDir(), "tool-display")
	if err != nil {
		t.Fatal(err)
	}
	if err := logger.AppendPrompt("inspect", nil); err != nil {
		t.Fatal(err)
	}
	if err := logger.Append("model_response", map[string]any{
		"response_id": "r1", "text": "before", "tool_call_count": 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := logger.Append("tool_call", map[string]any{
		"call_id": "call-1", "name": "shell", "arguments": json.RawMessage(`{"command":"check"}`),
	}); err != nil {
		t.Fatal(err)
	}
	output := strings.Repeat("result line\n", toolCompactLines+1)
	if err := logger.Append("tool_result", map[string]any{
		"call_id": "call-1", "name": "shell", "output": output, "failed": true,
		"image_count": 2,
	}); err != nil {
		t.Fatal(err)
	}
	if err := logger.Append("model_response", map[string]any{
		"response_id": "r2", "text": "after", "tool_call_count": 0,
	}); err != nil {
		t.Fatal(err)
	}
	path := logger.Path()
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}

	text, messages, expands, err := sessionDisplayTranscript(path)
	if err != nil {
		t.Fatal(err)
	}
	before, tool, after := strings.Index(text, "before"), strings.Index(text, "#### Tool failed: `shell`"), strings.Index(text, "after")
	if before < 0 || tool <= before || after <= tool {
		t.Fatalf("tool order was not restored:\n%s", text)
	}
	if !strings.Contains(text, "2 image attachment(s)") || !strings.Contains(text, "output folded") {
		t.Fatalf("persisted metadata or compact output missing:\n%s", text)
	}
	if len(expands) != 1 || !strings.Contains(expands[0], strings.Repeat("result line\n", toolCompactLines)) {
		t.Fatalf("full output was not retained: %#v", expands)
	}
	if len(messages) != 2 || messages[0].role != "user" || messages[1].role != "assistant" ||
		text[messages[0].start:messages[0].offset] != "You" ||
		text[messages[1].start:messages[1].offset] != "Gork" ||
		messages[0].at.IsZero() || messages[1].at.Before(messages[0].at) {
		t.Fatalf("timestamp labels were not restored: %#v", messages)
	}
}

func TestSessionDisplayTranscriptKeepsSyntheticAssistantBoundary(t *testing.T) {
	logger, err := session.NewLoggerWithID(t.TempDir(), "tool-boundary")
	if err != nil {
		t.Fatal(err)
	}
	if err := logger.AppendPrompt("start", nil); err != nil {
		t.Fatal(err)
	}
	for _, event := range []struct {
		kind string
		data map[string]any
	}{
		{"model_response", map[string]any{"response_id": "r1", "text": "first", "tool_call_count": 1}},
		{"user_prompt", map[string]any{"text": "internal", "synthetic": true}},
		{"model_response", map[string]any{"response_id": "r2", "text": "second", "tool_call_count": 0}},
	} {
		if err := logger.Append(event.kind, event.data); err != nil {
			t.Fatal(err)
		}
	}
	path := logger.Path()
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	text, messages, _, err := sessionDisplayTranscript(path)
	if err != nil || strings.Count(text, "Gork\n") != 2 || strings.Contains(text, "internal") || len(messages) != 3 {
		t.Fatalf("text=%q messages=%#v err=%v", text, messages, err)
	}
	if messages[1].at.After(time.Now()) {
		t.Fatalf("unexpected future timestamp: %v", messages[1].at)
	}
}
