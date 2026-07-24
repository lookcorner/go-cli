package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/lookcorner/go-cli/internal/api"
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
