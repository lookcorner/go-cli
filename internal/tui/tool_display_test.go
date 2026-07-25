package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

func TestToolVerbGroupLabelPreservesBucketOrderAndFailures(t *testing.T) {
	members := []toolVerbMember{
		{kind: toolVerbFile},
		{kind: toolVerbSearch, failed: true},
		{kind: toolVerbFile},
		{kind: toolVerbDir},
	}
	if got, want := toolVerbGroupLabel(members), "Read 2 files, Searched 1 pattern, Listed 1 dir · 1 failed"; got != want {
		t.Fatalf("label=%q want=%q", got, want)
	}
	if kind, ok := classifyToolVerb("read_file", json.RawMessage(`{"target_file":"/tmp/skills/deploy/SKILL.md"}`)); !ok || kind != toolVerbSkill {
		t.Fatalf("skill classification=%q ok=%v", kind, ok)
	}
	if _, ok := classifyToolVerb("edit_file", json.RawMessage(`{"path":"main.go"}`)); ok {
		t.Fatal("edit tool joined a verb group")
	}
}

func TestToolVerbGroupCountsDistinctWebSearchCitations(t *testing.T) {
	members := []toolVerbMember{
		{kind: toolVerbWebSearch, citations: []string{"https://a.example/1", "https://b.example/"}},
		{kind: toolVerbWebSearch, citations: []string{"https://a.example/1", "https://c.example/"}},
	}
	if got, want := toolVerbGroupLabel(members), "Searched 3 websites"; got != want {
		t.Fatalf("label=%q want=%q", got, want)
	}
	members = []toolVerbMember{
		{kind: toolVerbWebSearch},
		{kind: toolVerbWebSearch, failed: true, citations: []string{"https://ignored.example/"}},
	}
	if got, want := toolVerbGroupLabel(members), "Searched 2 websites · 1 failed"; got != want {
		t.Fatalf("fallback label=%q want=%q", got, want)
	}
}

func TestLiveToolVerbGroupCountsDistinctWebSearchCitations(t *testing.T) {
	m := &model{groupToolVerbs: true, width: 80, height: 20}
	for index, citations := range [][]string{
		{"https://a.example/", "https://b.example/"},
		{"https://b.example/", "https://c.example/"},
	} {
		m.finishTool(toolFinishedEvent{
			call: api.ToolCall{
				CallID: fmt.Sprintf("call-%d", index),
				Name:   "web_search",
				Arguments: json.RawMessage(
					fmt.Sprintf(`{"query":"query %d"}`, index),
				),
			},
			result: tools.ExecutionResult{Output: "results", Citations: citations},
		})
	}
	if got, want := m.transcript.String(), "Searched 3 websites\n"; got != want {
		t.Fatalf("transcript=%q want=%q", got, want)
	}
}

func TestFullscreenToolVerbGroupUpdatesInPlaceAndEndsAtEdit(t *testing.T) {
	m := &model{groupToolVerbs: true, width: 80, height: 20}
	m.finishTool(toolFinishedEvent{
		call:   api.ToolCall{Name: "read_file", Arguments: json.RawMessage(`{"target_file":"a.go"}`)},
		result: tools.ExecutionResult{Output: "package a"},
	})
	m.finishTool(toolFinishedEvent{
		call:   api.ToolCall{Name: "grep", Arguments: json.RawMessage(`{"pattern":"TODO"}`)},
		result: tools.ExecutionResult{Output: "a.go:1:TODO"},
	})
	text := m.transcript.String()
	if text != "Read 1 file, Searched 1 pattern\n" || len(m.toolExpand) != 1 {
		t.Fatalf("group text=%q expansions=%d", text, len(m.toolExpand))
	}
	if !strings.Contains(m.toolExpand[0], "read_file") || !strings.Contains(m.toolExpand[0], "grep") {
		t.Fatalf("group expansion=%q", m.toolExpand[0])
	}

	m.finishTool(toolFinishedEvent{
		call:   api.ToolCall{Name: "edit_file", Arguments: json.RawMessage(`{"path":"a.go","old_text":"a","new_text":"b"}`)},
		result: tools.ExecutionResult{Output: "edited a.go (1 replacement(s))"},
	})
	if m.toolVerbGroup != nil || strings.Count(m.transcript.String(), "Read 1 file") != 1 ||
		!strings.Contains(m.transcript.String(), "#### Tool: `edit_file`") {
		t.Fatalf("edit did not end group:\n%s", m.transcript.String())
	}
}

func TestMinimalToolVerbGroupPrintsOnceAtBoundary(t *testing.T) {
	m := &model{minimal: true, groupToolVerbs: true, width: 80, height: 20}
	m.finishTool(toolFinishedEvent{
		call:   api.ToolCall{Name: "read_file", Arguments: json.RawMessage(`{"target_file":"a.go"}`)},
		result: tools.ExecutionResult{Output: "package a"},
	})
	m.finishTool(toolFinishedEvent{
		call:   api.ToolCall{Name: "read_file", Arguments: json.RawMessage(`{"target_file":"b.go"}`)},
		result: tools.ExecutionResult{Output: "package b"},
	})
	if m.transcript.Len() != 0 || len(m.toolExpand) != 0 {
		t.Fatalf("minimal group printed before boundary: %q", m.transcript.String())
	}
	m.finishToolVerbGroup()
	if got := m.transcript.String(); got != "Read 2 files\n" {
		t.Fatalf("minimal group=%q", got)
	}
	if len(m.toolExpand) != 1 || m.minimalFlushTo != m.transcript.Len() {
		t.Fatalf("expansions=%d flush=%d len=%d", len(m.toolExpand), m.minimalFlushTo, m.transcript.Len())
	}
}

func TestCollapsedEditBlockUsesExactDiffstatAndKeepsExpansion(t *testing.T) {
	m := &model{minimal: true, collapsedEditBlocks: true, width: 80, height: 20}
	m.finishTool(toolFinishedEvent{
		call: api.ToolCall{
			Name:      "search_replace",
			Arguments: json.RawMessage(`{"file_path":"internal/greet.py","old_string":"return \"hi\"","new_string":"name = \"grok\"\nreturn name"}`),
		},
		result: tools.ExecutionResult{Output: "edited internal/greet.py (1 replacement(s))"},
	})
	m.finishCollapsedEditGroup()
	text := m.transcript.String()
	if !strings.Contains(text, "Edit `greet.py` +2/-1") || strings.Contains(text, "Arguments") || len(m.toolExpand) != 1 {
		t.Fatalf("collapsed edit=%q expansions=%d", text, len(m.toolExpand))
	}
	m.expandLastTool()
	if !strings.Contains(m.transcript.String(), "search_replace") || !strings.Contains(m.transcript.String(), "return name") {
		t.Fatalf("expanded edit lost full tool details:\n%s", m.transcript.String())
	}
}

func TestCollapsedEditBlocksCoalesceAdjacentSameFile(t *testing.T) {
	root := t.TempDir()
	m := &model{collapsedEditBlocks: true, workspace: root, width: 80, height: 20}
	for _, test := range []struct {
		path, oldText, newText string
	}{
		{path: "src/main.go", oldText: "old", newText: "new\nline"},
		{path: filepath.Join(root, "src", "main.go"), oldText: "other\nold", newText: "other"},
	} {
		m.finishTool(toolFinishedEvent{
			call: api.ToolCall{
				Name: "edit_file",
				Arguments: json.RawMessage(fmt.Sprintf(
					`{"path":%q,"old_text":%q,"new_text":%q}`, test.path, test.oldText, test.newText,
				)),
			},
			result: tools.ExecutionResult{Output: "edited " + test.path + " (1 replacement(s))"},
		})
	}
	if got, want := m.transcript.String(), "Edit `main.go` +3/-3\n"; got != want {
		t.Fatalf("coalesced edit=%q want=%q", got, want)
	}
	if len(m.toolExpand) != 1 || strings.Count(m.toolExpand[0], "#### Tool: `edit_file`") != 2 {
		t.Fatalf("expansion=%#v", m.toolExpand)
	}
	m.finishTool(toolFinishedEvent{
		call:   api.ToolCall{Name: "shell", Arguments: json.RawMessage(`{"command":"true"}`)},
		result: tools.ExecutionResult{Output: "ok"},
	})
	if m.collapsedEditGroup != nil || strings.Count(m.transcript.String(), "Edit `main.go`") != 1 {
		t.Fatalf("boundary did not finish edit group:\n%s", m.transcript.String())
	}
}

func TestCollapsedEditBlocksKeepDifferentFilesAndFailuresSeparate(t *testing.T) {
	m := &model{collapsedEditBlocks: true, width: 80, height: 20}
	m.finishTool(toolFinishedEvent{
		call:   api.ToolCall{Name: "edit_file", Arguments: json.RawMessage(`{"path":"a.go","old_text":"a","new_text":"b"}`)},
		result: tools.ExecutionResult{Output: "edited a.go (1 replacement(s))"},
	})
	m.finishTool(toolFinishedEvent{
		call:   api.ToolCall{Name: "edit_file", Arguments: json.RawMessage(`{"path":"b.go","old_text":"a","new_text":"b"}`)},
		result: tools.ExecutionResult{Output: "edited b.go (1 replacement(s))"},
	})
	m.finishTool(toolFinishedEvent{
		call: api.ToolCall{Name: "edit_file", Arguments: json.RawMessage(`{"path":"b.go","old_text":"b","new_text":"c"}`)},
		err:  errors.New("stale"),
	})
	if text := m.transcript.String(); strings.Count(text, "Edit `a.go`") != 1 || strings.Count(text, "Edit `b.go`") != 1 ||
		!strings.Contains(text, "#### Tool failed: `edit_file`") {
		t.Fatalf("separate edits:\n%s", text)
	}
}

func TestMinimalCollapsedEditGroupPrintsAtBoundary(t *testing.T) {
	m := &model{minimal: true, collapsedEditBlocks: true, width: 80, height: 20}
	for _, oldText := range []string{"a", "b"} {
		m.finishTool(toolFinishedEvent{
			call: api.ToolCall{Name: "edit_file", Arguments: json.RawMessage(
				fmt.Sprintf(`{"path":"main.go","old_text":%q,"new_text":"next"}`, oldText),
			)},
			result: tools.ExecutionResult{Output: "edited main.go (1 replacement(s))"},
		})
	}
	if m.transcript.Len() != 0 || len(m.toolExpand) != 0 {
		t.Fatalf("minimal edit printed before boundary: %q", m.transcript.String())
	}
	m.finishCollapsedEditGroup()
	if got := m.transcript.String(); got != "Edit `main.go` +2/-2\n" {
		t.Fatalf("minimal group=%q", got)
	}
	if len(m.toolExpand) != 1 || strings.Count(m.toolExpand[0], "#### Tool: `edit_file`") != 2 {
		t.Fatalf("minimal expansion=%#v", m.toolExpand)
	}
}

func TestCollapsedEditBlockCountsReplaceAllAndHashlineRanges(t *testing.T) {
	tests := []struct {
		name   string
		tool   string
		args   string
		output string
		want   string
	}{
		{
			name:   "replace all",
			tool:   "edit_file",
			args:   `{"path":"repeat.txt","old_text":"old\nvalue\n","new_text":"new\n","replace_all":true}`,
			output: "edited repeat.txt (3 replacement(s))",
			want:   "Edit `repeat.txt` +3/-6",
		},
		{
			name: "hashline range and insert",
			tool: "hashline_edit",
			args: `{"file_path":"main.go","edits":[` +
				`{"op":"replace","anchor":"2:aaa","end_anchor":"4:bbb","content":"one\ntwo"},` +
				`{"op":"insert_after","anchor":"8:ccc","content":"three"}]}`,
			output: "applied 2 edit(s) to main.go",
			want:   "Edit `main.go` +3/-3",
		},
		{
			name:   "hashline empty line insert",
			tool:   "hashline_edit",
			args:   `{"file_path":"main.go","edits":[{"op":"insert_after","anchor":"2:aaa","content":""}]}`,
			output: "applied 1 edit(s) to main.go",
			want:   "Edit `main.go` +1/-0",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := collapsedEditSummary(test.tool, json.RawMessage(test.args), test.output, false)
			if !ok || got != test.want {
				t.Fatalf("summary=%q ok=%v want=%q", got, ok, test.want)
			}
		})
	}
}

func TestCollapsedEditBlockLeavesFailuresAndUnknownWritesExpanded(t *testing.T) {
	for _, test := range []struct {
		name   string
		tool   string
		args   string
		output string
		failed bool
	}{
		{name: "failure", tool: "edit_file", args: `{"path":"main.go","old_text":"a","new_text":"b"}`, failed: true},
		{name: "whole file write", tool: "write_file", args: `{"path":"main.go","content":"package main"}`, output: "wrote 12 bytes to main.go"},
		{name: "hashline whole file write", tool: "hashline_edit", args: `{"file_path":"main.go","edits":[{"op":"write","content":"package main"}]}`, output: "applied 1 edit(s) to main.go"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got, ok := collapsedEditSummary(test.tool, json.RawMessage(test.args), test.output, test.failed); ok {
				t.Fatalf("unexpected summary %q", got)
			}
		})
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

func TestFullscreenScrollbackTogglesVisibleToolFold(t *testing.T) {
	m := &model{width: 80, height: 20, scrollFocused: true, groupToolVerbs: true}
	m.finishTool(toolFinishedEvent{
		call:   api.ToolCall{Name: "read_file", Arguments: json.RawMessage(`{"path":"main.go"}`)},
		result: tools.ExecutionResult{Output: "package main"},
	})
	m.finishToolVerbGroup()
	collapsed := m.transcript.String()
	if len(m.toolFolds) != 1 || !strings.Contains(collapsed, "Read 1 file") {
		t.Fatalf("folds=%#v transcript=%q", m.toolFolds, collapsed)
	}

	updated, command := m.handleKey(tea.KeyPressMsg(tea.Key{Code: 'e', Text: "e"}))
	m = updated.(*model)
	if command != nil || !strings.Contains(m.transcript.String(), "#### Tool: `read_file`") ||
		m.status != "tool group expanded" || !m.toolFolds[0].expanded {
		t.Fatalf("command=%v status=%q fold=%#v transcript=%q", command != nil, m.status, m.toolFolds[0], m.transcript.String())
	}

	updated, command = m.handleKey(tea.KeyPressMsg(tea.Key{Code: 'e', Text: "e"}))
	m = updated.(*model)
	if command != nil || m.transcript.String() != collapsed || m.status != "tool group collapsed" || m.toolFolds[0].expanded {
		t.Fatalf("command=%v status=%q fold=%#v transcript=%q", command != nil, m.status, m.toolFolds[0], m.transcript.String())
	}
}

func TestFullscreenScrollbackExpandRequiresVisibleFold(t *testing.T) {
	m := &model{width: 80, height: 5, scrollFocused: true}
	m.transcript.WriteString("plain transcript")
	updated, command := m.handleKey(tea.KeyPressMsg(tea.Key{Code: 'e', Text: "e"}))
	m = updated.(*model)
	if command != nil || m.status != "no folded tool group in view" || !m.scrollFocused {
		t.Fatalf("command=%v status=%q focused=%v", command != nil, m.status, m.scrollFocused)
	}
}

func TestFullscreenToolFoldKeepsLaterOffsetsAndTimestamps(t *testing.T) {
	m := &model{width: 80, height: 40, scrollFocused: true, showTimestamps: true}
	m.transcript.WriteString("Gork\n")
	m.transcriptMessages = []transcriptMessage{{start: 0, offset: 4, at: time.Date(2026, 7, 25, 12, 0, 0, 0, time.Local), role: "assistant"}}
	firstStart := m.transcript.Len()
	m.appendToolDisplay("First folded")
	first := m.rememberToolFold(firstStart, "#### Tool: `first`\n\nfull first")
	m.appendToolDisplay("between")
	secondStart := m.transcript.Len()
	m.appendToolDisplay("Second folded")
	second := m.rememberToolFold(secondStart, "#### Tool: `second`\n\nfull second")

	if !m.toggleVisibleToolFold() || !m.toolFolds[second].expanded || m.toolFolds[first].expanded {
		t.Fatalf("first=%#v second=%#v transcript=%q", m.toolFolds[first], m.toolFolds[second], m.transcript.String())
	}
	if !strings.Contains(m.transcriptText(), "12:00 PM") || !strings.Contains(m.transcript.String(), "full second") {
		t.Fatalf("timestamp or expansion lost: %q", m.transcriptText())
	}
	if !m.toggleVisibleToolFold() || m.toolFolds[second].expanded {
		t.Fatalf("second fold did not collapse: %#v", m.toolFolds[second])
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

	text, messages, expands, folds, err := sessionDisplayTranscript(path, "", false, false, true)
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
	if len(folds) != 1 || folds[0].collapsed == "" || !strings.Contains(folds[0].full, "result line") {
		t.Fatalf("folds=%#v", folds)
	}
	if len(messages) != 2 || messages[0].role != "user" || messages[1].role != "assistant" ||
		text[messages[0].start:messages[0].offset] != "You" ||
		text[messages[1].start:messages[1].offset] != "Gork" ||
		messages[0].at.IsZero() || messages[1].at.Before(messages[0].at) {
		t.Fatalf("timestamp labels were not restored: %#v", messages)
	}
}

func TestSessionDisplayTranscriptRestoresCollapsedEdit(t *testing.T) {
	logger, err := session.NewLoggerWithID(t.TempDir(), "collapsed-edit")
	if err != nil {
		t.Fatal(err)
	}
	if err := logger.AppendPrompt("edit", nil); err != nil {
		t.Fatal(err)
	}
	if err := logger.Append("model_response", map[string]any{"response_id": "r1", "tool_call_count": 1}); err != nil {
		t.Fatal(err)
	}
	if err := logger.Append("tool_call", map[string]any{
		"call_id": "call-1", "name": "edit_file",
		"arguments": json.RawMessage(`{"path":"src/main.go","old_text":"old","new_text":"new\nline"}`),
	}); err != nil {
		t.Fatal(err)
	}
	if err := logger.Append("tool_result", map[string]any{
		"call_id": "call-1", "name": "edit_file", "output": "edited src/main.go (1 replacement(s))",
	}); err != nil {
		t.Fatal(err)
	}
	if err := logger.Append("model_response", map[string]any{"response_id": "r2", "text": "done", "tool_call_count": 0}); err != nil {
		t.Fatal(err)
	}
	path := logger.Path()
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}

	text, _, expands, _, err := sessionDisplayTranscript(path, "", true, false, true)
	if err != nil || !strings.Contains(text, "Edit `main.go` +2/-1") || strings.Contains(text, "Arguments") {
		t.Fatalf("text=%q err=%v", text, err)
	}
	if len(expands) != 1 || !strings.Contains(expands[0], `"old_text": "old"`) {
		t.Fatalf("expansions=%#v", expands)
	}
}

func TestSessionDisplayTranscriptCanHideAndRestoreThoughts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "thought.jsonl")
	content := "" +
		`{"kind":"user_prompt","data":{"text":"question"}}` + "\n" +
		`{"kind":"model_thought","data":{"text":"inspect\ninputs"}}` + "\n" +
		`{"kind":"model_response","data":{"response_id":"r1","text":"answer","tool_call_count":0}}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	shown, _, _, _, err := sessionDisplayTranscript(path, "", false, false, true)
	if err != nil || !strings.Contains(shown, "> Thinking\n>\n> inspect\n> inputs") || !strings.Contains(shown, "answer") {
		t.Fatalf("shown=%q err=%v", shown, err)
	}
	hidden, _, _, _, err := sessionDisplayTranscript(path, "", false, false, false)
	if err != nil || strings.Contains(hidden, "Thinking") || strings.Contains(hidden, "inspect") || !strings.Contains(hidden, "answer") {
		t.Fatalf("hidden=%q err=%v", hidden, err)
	}
}

func TestSessionDisplayTranscriptCoalescesAdjacentSameFileEdits(t *testing.T) {
	logger, err := session.NewLoggerWithID(t.TempDir(), "coalesced-edits")
	if err != nil {
		t.Fatal(err)
	}
	if err := logger.AppendPrompt("edit", nil); err != nil {
		t.Fatal(err)
	}
	if err := logger.Append("model_response", map[string]any{"response_id": "r1", "tool_call_count": 2}); err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	for index, editPath := range []string{"src/main.go", filepath.Join(workspace, "src", "main.go")} {
		id := fmt.Sprintf("call-%d", index)
		if err := logger.Append("tool_call", map[string]any{
			"call_id": id, "name": "edit_file",
			"arguments": json.RawMessage(fmt.Sprintf(`{"path":%q,"old_text":"old","new_text":"new"}`, editPath)),
		}); err != nil {
			t.Fatal(err)
		}
		if err := logger.Append("tool_result", map[string]any{
			"call_id": id, "name": "edit_file", "output": "edited " + editPath + " (1 replacement(s))",
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := logger.Append("model_response", map[string]any{"response_id": "r2", "text": "done", "tool_call_count": 0}); err != nil {
		t.Fatal(err)
	}
	path := logger.Path()
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	text, _, expands, _, err := sessionDisplayTranscript(path, workspace, true, false, true)
	if err != nil || strings.Count(text, "Edit `main.go`") != 1 || !strings.Contains(text, "Edit `main.go` +2/-2") {
		t.Fatalf("text=%q err=%v", text, err)
	}
	if len(expands) != 1 || strings.Count(expands[0], "#### Tool: `edit_file`") != 2 {
		t.Fatalf("expansions=%#v", expands)
	}
}

func TestSessionDisplayTranscriptGroupsConsecutiveToolVerbs(t *testing.T) {
	logger, err := session.NewLoggerWithID(t.TempDir(), "tool-verbs")
	if err != nil {
		t.Fatal(err)
	}
	if err := logger.AppendPrompt("inspect", nil); err != nil {
		t.Fatal(err)
	}
	if err := logger.Append("model_response", map[string]any{"response_id": "r1", "text": "checking", "tool_call_count": 1}); err != nil {
		t.Fatal(err)
	}
	tools := []struct {
		id, name, args, output string
		failed                 bool
	}{
		{"read-1", "read_file", `{"target_file":"a.go"}`, "package a", false},
		{"read-2", "read_file", `{"target_file":"skills/test/SKILL.md"}`, "# Skill", false},
		{"grep-1", "grep", `{"pattern":"TODO"}`, "a.go:1:TODO", true},
		{"edit-1", "edit_file", `{"path":"a.go","old_text":"a","new_text":"b"}`, "edited a.go (1 replacement(s))", false},
		{"read-3", "hashline_read", `{"target_file":"b.go"}`, "1:abc:package b", false},
	}
	for _, tool := range tools {
		if err := logger.Append("tool_call", map[string]any{
			"call_id": tool.id, "name": tool.name, "arguments": json.RawMessage(tool.args),
		}); err != nil {
			t.Fatal(err)
		}
		if err := logger.Append("tool_result", map[string]any{
			"call_id": tool.id, "name": tool.name, "output": tool.output, "failed": tool.failed,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := logger.Append("model_response", map[string]any{"response_id": "r2", "text": "done", "tool_call_count": 0}); err != nil {
		t.Fatal(err)
	}
	path := logger.Path()
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}

	text, _, expands, _, err := sessionDisplayTranscript(path, "", true, true, true)
	if err != nil {
		t.Fatal(err)
	}
	first := "Read 1 file, Read 1 skill, Searched 1 pattern · 1 failed"
	if !strings.Contains(text, "checking\n\n"+first) || strings.Count(text, first) != 1 || !strings.Contains(text, "Edit `a.go` +1/-1") ||
		strings.Count(text, "Read 1 file") != 2 || !strings.Contains(text, "done") {
		t.Fatalf("grouped transcript:\n%s", text)
	}
	if len(expands) != 3 || !strings.Contains(expands[0], "read_file") || !strings.Contains(expands[0], "grep") {
		t.Fatalf("expansions=%#v", expands)
	}

	ungrouped, _, _, _, err := sessionDisplayTranscript(path, "", true, false, true)
	if err != nil || strings.Contains(ungrouped, first) || !strings.Contains(ungrouped, "#### Tool failed: `grep`") {
		t.Fatalf("ungrouped transcript=%q err=%v", ungrouped, err)
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
	text, messages, _, _, err := sessionDisplayTranscript(path, "", false, false, true)
	if err != nil || strings.Count(text, "Gork\n") != 2 || strings.Contains(text, "internal") || len(messages) != 3 {
		t.Fatalf("text=%q messages=%#v err=%v", text, messages, err)
	}
	if messages[1].at.After(time.Now()) {
		t.Fatalf("unexpected future timestamp: %v", messages[1].at)
	}
}
