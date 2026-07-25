package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/session"
	"github.com/lookcorner/go-cli/internal/tools"
	"github.com/lookcorner/go-cli/internal/workspace"
)

type settingsQuestionObserver struct{ hasDeadline bool }

func (o *settingsQuestionObserver) AskUserQuestion(ctx context.Context, _ tools.UserQuestionRequest) (tools.UserQuestionResponse, error) {
	_, o.hasDeadline = ctx.Deadline()
	return tools.UserQuestionResponse{Outcome: "cancelled"}, nil
}

func TestSettingsCommandAliasesOpenAndClose(t *testing.T) {
	for _, prompt := range []string{"/settings", "/config ignored", "/preferences", "/prefs anything"} {
		m := &model{width: 70, height: 16, themeName: "groknight", theme: paletteFor("groknight")}
		m.setInput(prompt)
		updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
		m = updated.(*model)
		if command != nil || m.settings == nil || m.running || m.status != "settings" {
			t.Fatalf("prompt=%q command=%v settings=%v running=%v status=%q", prompt, command != nil, m.settings != nil, m.running, m.status)
		}
		if content := stripUIANSI(m.View().Content); !strings.Contains(content, "Settings") || !strings.Contains(content, "Timestamps: off") || !strings.Contains(content, "Group tool verbs: off") {
			t.Fatalf("prompt=%q content=%q", prompt, content)
		}
		updated, command = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEsc}))
		m = updated.(*model)
		if command != nil || m.settings != nil || m.status != "ready" {
			t.Fatalf("prompt=%q command=%v settings=%v status=%q", prompt, command != nil, m.settings != nil, m.status)
		}
	}
}

func TestSettingsPanelHidesConversationTimeline(t *testing.T) {
	m := &model{width: 70, height: 16, showTimeline: true, settings: &settingsState{}}
	m.transcriptMessages = []transcriptMessage{{start: 0, role: "user"}, {start: 2, role: "user"}}
	if m.timelineWidth() != 0 {
		t.Fatal("settings panel exposed conversation timeline")
	}
}

func TestSettingsPanelPersistsEverySupportedSetting(t *testing.T) {
	var booleans []string
	var themes []string
	var screenModes []string
	var mermaidModes []string
	var selectionModes []string
	var scrollModes []string
	var scrollSpeeds []uint8
	var scrollLines []uint8
	m := &model{
		width: 70, height: 18, themeName: "groknight", theme: paletteFor("groknight"), mermaidMode: "auto",
		scrollSpeed: 50, scrollLines: 3, scrollInput: scrollInput{mode: "auto"}, settings: &settingsState{},
		persistTimestamps: func(value bool) error { booleans = append(booleans, "timestamps"); return nil },
		persistTimeline:   func(value bool) error { booleans = append(booleans, "timeline"); return nil },
		persistCompactMode: func(value bool) error {
			booleans = append(booleans, "compact")
			return nil
		},
		persistVimMode: func(value bool) error { booleans = append(booleans, "vim"); return nil },
		persistGroupTools: func(value bool) error {
			booleans = append(booleans, "group")
			return nil
		},
		persistEditBlocks: func(value bool) error {
			booleans = append(booleans, "edits")
			return nil
		},
		persistSuggestions: func(value bool) error {
			booleans = append(booleans, "suggestions")
			return nil
		},
		persistRemember: func(value bool) error {
			booleans = append(booleans, "remember")
			return nil
		},
		persistQuestionTime: func(value bool) error {
			booleans = append(booleans, "question-timeout")
			return nil
		},
		persistInvertScroll: func(value bool) error {
			booleans = append(booleans, "invert-scroll")
			return nil
		},
		persistSelection: func(value string) error {
			selectionModes = append(selectionModes, value)
			return nil
		},
		persistScrollMode: func(value string) error {
			scrollModes = append(scrollModes, value)
			return nil
		},
		persistScrollSpeed: func(value uint8) error {
			scrollSpeeds = append(scrollSpeeds, value)
			return nil
		},
		persistScrollLines: func(value uint8) error {
			scrollLines = append(scrollLines, value)
			return nil
		},
		persistScreenMode: func(value string) error {
			screenModes = append(screenModes, value)
			return nil
		},
		persistMermaid: func(value string) error { mermaidModes = append(mermaidModes, value); return nil },
		persistTheme:   func(value string) error { themes = append(themes, value); return nil },
	}
	for index := 0; index < settingsCount; index++ {
		m.settings.selected = index
		updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
		m = updated.(*model)
		if index == 12 || index == 14 {
			if command != nil || m.settings.number == nil || m.status != "editing setting" {
				t.Fatalf("index=%d did not open number editor: command=%v settings=%#v status=%q", index, command != nil, m.settings, m.status)
			}
			updated, command = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyUp}))
			m = updated.(*model)
			updated, command = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
			m = updated.(*model)
		}
		wantStatus := "settings updated"
		if index == 8 || index == 9 {
			wantStatus = "settings updated; restart to apply"
		}
		if command != nil || m.settings.err != "" || m.status != wantStatus {
			t.Fatalf("index=%d command=%v err=%q status=%q", index, command != nil, m.settings.err, m.status)
		}
	}
	if !m.showTimestamps || !m.showTimeline || !m.compactMode || !m.vimMode || !m.defaultMinimal || !m.groupToolVerbs || !m.collapsedEditBlocks || !m.suggestionsEnabled || !m.rememberApprovals || !m.questionTimeout || !m.multiline || !m.invertScroll ||
		strings.Join(booleans, ",") != "timestamps,timeline,compact,vim,group,edits,suggestions,remember,question-timeout,invert-scroll" || strings.Join(screenModes, ",") != "minimal" {
		t.Fatalf("timestamps=%v timeline=%v compact=%v vim=%v persisted=%v", m.showTimestamps, m.showTimeline, m.compactMode, m.vimMode, booleans)
	}
	if m.themeName != "grokday" || m.theme.name != "grokday" || strings.Join(themes, ",") != "grokday" {
		t.Fatalf("theme=%q palette=%q persisted=%v", m.themeName, m.theme.name, themes)
	}
	if m.mermaidMode != "on" || strings.Join(mermaidModes, ",") != "on" {
		t.Fatalf("Mermaid=%q persisted=%v", m.mermaidMode, mermaidModes)
	}
	if m.selectionMode != selectionHold || strings.Join(selectionModes, ",") != "hold" {
		t.Fatalf("selection=%q persisted=%v", m.selectionMode.canonical(), selectionModes)
	}
	if m.scrollInput.mode != "wheel" || strings.Join(scrollModes, ",") != "wheel" {
		t.Fatalf("scroll mode=%q persisted=%v", m.scrollInput.mode, scrollModes)
	}
	if m.scrollSpeed != 51 || m.scrollLines != 4 || !reflect.DeepEqual(scrollSpeeds, []uint8{51}) || !reflect.DeepEqual(scrollLines, []uint8{4}) {
		t.Fatalf("scroll speed=%d lines=%d persisted=%v/%v", m.scrollSpeed, m.scrollLines, scrollSpeeds, scrollLines)
	}
}

func TestSettingsPanelRollsBackFailedPersistence(t *testing.T) {
	m := &model{
		width: 60, height: 16, showTimeline: true, themeName: "auto", theme: paletteFor("auto"), settings: &settingsState{selected: 1},
		persistTimeline: func(bool) error { return errors.New("disk full") },
	}
	updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeySpace}))
	m = updated.(*model)
	if command != nil || !m.showTimeline || m.settings == nil || m.settings.err != "disk full" || m.status != "setting update failed" {
		t.Fatalf("command=%v timeline=%v settings=%#v status=%q", command != nil, m.showTimeline, m.settings, m.status)
	}
	if content := stripUIANSI(m.View().Content); !strings.Contains(content, "Error: disk full") {
		t.Fatalf("content=%q", content)
	}

	m.settings.selected = 4
	m.persistScreenMode = func(string) error { return errors.New("read only") }
	updated, command = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command != nil || m.defaultMinimal || m.settings.err != "read only" {
		t.Fatalf("command=%v minimal=%v err=%q", command != nil, m.defaultMinimal, m.settings.err)
	}

	m.settings.selected = 16
	m.mermaidMode = "auto"
	m.persistMermaid = func(string) error { return errors.New("read only") }
	updated, command = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command != nil || m.mermaidMode != "auto" || m.settings.err != "read only" {
		t.Fatalf("command=%v Mermaid=%q err=%q", command != nil, m.mermaidMode, m.settings.err)
	}

	m.settings.selected = 17
	m.persistTheme = func(string) error { return errors.New("read only") }
	updated, command = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command != nil || m.themeName != "auto" || m.settings.err != "read only" {
		t.Fatalf("command=%v theme=%q err=%q", command != nil, m.themeName, m.settings.err)
	}

	m.settings.selected = 5
	m.groupToolVerbs = true
	m.persistGroupTools = func(bool) error { return errors.New("read only") }
	updated, command = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command != nil || !m.groupToolVerbs || m.settings.err != "read only" {
		t.Fatalf("command=%v grouped=%v err=%q", command != nil, m.groupToolVerbs, m.settings.err)
	}

	m.settings.selected = 6
	m.persistEditBlocks = func(bool) error { return errors.New("read only") }
	updated, command = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command != nil || m.collapsedEditBlocks || m.settings.err != "read only" {
		t.Fatalf("command=%v collapsed=%v err=%q", command != nil, m.collapsedEditBlocks, m.settings.err)
	}

	m.settings.selected = 7
	m.suggestionsEnabled = true
	m.promptSuggestion = "run tests"
	m.persistSuggestions = func(bool) error { return errors.New("read only") }
	updated, command = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command != nil || !m.suggestionsEnabled || m.promptSuggestion != "run tests" || m.settings.err != "read only" {
		t.Fatalf("command=%v enabled=%v suggestion=%q err=%q", command != nil, m.suggestionsEnabled, m.promptSuggestion, m.settings.err)
	}

	m.settings.selected = 8
	m.persistRemember = func(bool) error { return errors.New("read only") }
	updated, command = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command != nil || m.rememberApprovals || m.settings.err != "read only" {
		t.Fatalf("command=%v remember=%v err=%q", command != nil, m.rememberApprovals, m.settings.err)
	}

	m.settings.selected = 9
	m.questionTimeout = true
	m.persistQuestionTime = func(bool) error { return errors.New("read only") }
	updated, command = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command != nil || !m.questionTimeout || m.settings.err != "read only" {
		t.Fatalf("command=%v timeout=%v err=%q", command != nil, m.questionTimeout, m.settings.err)
	}

	m.settings.selected = 11
	m.persistInvertScroll = func(bool) error { return errors.New("read only") }
	updated, command = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command != nil || m.invertScroll || m.settings.err != "read only" {
		t.Fatalf("command=%v invert=%v err=%q", command != nil, m.invertScroll, m.settings.err)
	}

	m.settings.selected = 15
	m.persistSelection = func(string) error { return errors.New("read only") }
	updated, command = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command != nil || m.selectionMode != selectionFlash || m.settings.err != "read only" {
		t.Fatalf("command=%v selection=%q err=%q", command != nil, m.selectionMode.canonical(), m.settings.err)
	}

	m.settings.selected = 13
	m.scrollInput = scrollInput{mode: "auto", events: 2}
	m.persistScrollMode = func(string) error { return errors.New("read only") }
	updated, command = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command != nil || m.scrollInput.mode != "auto" || m.scrollInput.events != 2 || m.settings.err != "read only" {
		t.Fatalf("command=%v scroll input=%#v err=%q", command != nil, m.scrollInput, m.settings.err)
	}

	m.scrollSpeed = 50
	m.scrollCarry = 0.75
	m.settings.selected = 12
	m.persistScrollSpeed = func(uint8) error { return errors.New("read only") }
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyUp}))
	m = updated.(*model)
	updated, command = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command != nil || m.scrollSpeed != 50 || m.scrollCarry != 0.75 || m.settings.number != nil || m.settings.err != "read only" {
		t.Fatalf("command=%v speed=%d carry=%g settings=%#v", command != nil, m.scrollSpeed, m.scrollCarry, m.settings)
	}

	m.scrollLines = 3
	m.settings.selected = 14
	m.persistScrollLines = func(uint8) error { return errors.New("read only") }
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyUp}))
	m = updated.(*model)
	updated, command = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command != nil || m.scrollLines != 3 || m.settings.number != nil || m.settings.err != "read only" {
		t.Fatalf("command=%v lines=%d settings=%#v", command != nil, m.scrollLines, m.settings)
	}
}

func TestSettingsGroupToolVerbsRefoldsTranscriptImmediately(t *testing.T) {
	logger, err := session.NewLoggerWithID(t.TempDir(), "settings-groups")
	if err != nil {
		t.Fatal(err)
	}
	if err := logger.AppendPrompt("inspect", nil); err != nil {
		t.Fatal(err)
	}
	if err := logger.Append("model_response", map[string]any{
		"response_id": "response-1", "tool_call_count": 2,
	}); err != nil {
		t.Fatal(err)
	}
	for _, call := range []struct{ id, path string }{{"read-1", "a.go"}, {"read-2", "b.go"}} {
		if err := logger.Append("tool_call", map[string]any{
			"call_id": call.id, "name": "read_file",
			"arguments": json.RawMessage(`{"target_file":"` + call.path + `"}`),
		}); err != nil {
			t.Fatal(err)
		}
		if err := logger.Append("tool_result", map[string]any{
			"call_id": call.id, "name": "read_file", "output": "package test",
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := logger.Append("model_response", map[string]any{
		"response_id": "response-2", "text": "done", "tool_call_count": 0,
	}); err != nil {
		t.Fatal(err)
	}
	path := logger.Path()
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	grouped, messages, expands, err := sessionDisplayTranscript(path, "", false, true)
	if err != nil {
		t.Fatal(err)
	}
	persisted := true
	m := &model{
		width: 80, height: 20, runner: &agent.Runner{SessionPath: path},
		groupToolVerbs: true, settings: &settingsState{selected: 5},
		persistGroupTools: func(value bool) error { persisted = value; return nil },
	}
	m.replaceDisplayTranscript(grouped, messages, expands)
	if !strings.Contains(m.transcript.String(), "Read 2 files") {
		t.Fatalf("initial transcript=%q", m.transcript.String())
	}
	updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command != nil || m.groupToolVerbs || persisted || strings.Contains(m.transcript.String(), "Read 2 files") ||
		strings.Count(m.transcript.String(), "#### Tool: `read_file`") != 2 {
		t.Fatalf("command=%v grouped=%v persisted=%v transcript=%q", command != nil, m.groupToolVerbs, persisted, m.transcript.String())
	}
}

func TestSettingsGroupToolVerbsPreservesLocalTranscriptContent(t *testing.T) {
	logger, err := session.NewLoggerWithID(t.TempDir(), "settings-local-content")
	if err != nil {
		t.Fatal(err)
	}
	if err := logger.AppendPrompt("inspect", nil); err != nil {
		t.Fatal(err)
	}
	path := logger.Path()
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	m := &model{
		width: 80, height: 20, runner: &agent.Runner{SessionPath: path},
		groupToolVerbs: true, settings: &settingsState{selected: 5},
		persistGroupTools: func(bool) error { return nil },
	}
	m.transcript.WriteString("local help output")
	updated, _ := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if m.groupToolVerbs || m.transcript.String() != "local help output" {
		t.Fatalf("grouped=%v transcript=%q", m.groupToolVerbs, m.transcript.String())
	}
}

func TestSettingsCollapsedEditBlocksRefoldsTranscriptImmediately(t *testing.T) {
	logger, err := session.NewLoggerWithID(t.TempDir(), "settings-edits")
	if err != nil {
		t.Fatal(err)
	}
	if err := logger.AppendPrompt("edit", nil); err != nil {
		t.Fatal(err)
	}
	if err := logger.Append("model_response", map[string]any{
		"response_id": "response-1", "tool_call_count": 2,
	}); err != nil {
		t.Fatal(err)
	}
	for index, change := range []struct{ old, new string }{
		{"old", "new\nline"},
		{"next\nline", "next"},
	} {
		id := fmt.Sprintf("edit-%d", index)
		if err := logger.Append("tool_call", map[string]any{
			"call_id": id, "name": "edit_file",
			"arguments": json.RawMessage(fmt.Sprintf(
				`{"path":"main.go","old_text":%q,"new_text":%q}`, change.old, change.new,
			)),
		}); err != nil {
			t.Fatal(err)
		}
		if err := logger.Append("tool_result", map[string]any{
			"call_id": id, "name": "edit_file", "output": "edited main.go (1 replacement(s))",
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := logger.Append("model_response", map[string]any{
		"response_id": "response-2", "text": "done", "tool_call_count": 0,
	}); err != nil {
		t.Fatal(err)
	}
	path := logger.Path()
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	expanded, messages, expands, err := sessionDisplayTranscript(path, "", false, true)
	if err != nil {
		t.Fatal(err)
	}
	persisted := false
	m := &model{
		width: 80, height: 20, runner: &agent.Runner{SessionPath: path},
		groupToolVerbs: true, settings: &settingsState{selected: 6},
		persistEditBlocks: func(value bool) error { persisted = value; return nil },
	}
	m.replaceDisplayTranscript(expanded, messages, expands)
	if strings.Count(m.transcript.String(), "#### Tool: `edit_file`") != 2 {
		t.Fatalf("initial transcript=%q", m.transcript.String())
	}
	updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command != nil || !m.collapsedEditBlocks || !persisted ||
		!strings.Contains(m.transcript.String(), "Edit `main.go` +3/-3") ||
		strings.Contains(m.transcript.String(), "#### Tool: `edit_file`") {
		t.Fatalf("command=%v collapsed=%v persisted=%v transcript=%q", command != nil, m.collapsedEditBlocks, persisted, m.transcript.String())
	}
	updated, command = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command != nil || m.collapsedEditBlocks || persisted ||
		strings.Count(m.transcript.String(), "#### Tool: `edit_file`") != 2 {
		t.Fatalf("command=%v collapsed=%v persisted=%v transcript=%q", command != nil, m.collapsedEditBlocks, persisted, m.transcript.String())
	}
}

func TestSettingsPromptSuggestionsApplyImmediately(t *testing.T) {
	persisted := true
	m := &model{
		width: 60, height: 16, suggestionsEnabled: true, promptSuggestion: "run tests",
		settings:           &settingsState{selected: 7},
		persistSuggestions: func(value bool) error { persisted = value; return nil },
	}
	updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command != nil || m.suggestionsEnabled || persisted || m.promptSuggestion != "" || m.suggestionDismissed {
		t.Fatalf("command=%v enabled=%v persisted=%v suggestion=%q dismissed=%v", command != nil, m.suggestionsEnabled, persisted, m.promptSuggestion, m.suggestionDismissed)
	}
	updated, command = m.Update(promptSuggestionEvent{text: "stale", serial: m.promptSerial})
	m = updated.(*model)
	if command != nil || m.promptSuggestion != "" {
		t.Fatalf("disabled suggestion accepted: command=%v suggestion=%q", command != nil, m.promptSuggestion)
	}
	updated, command = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command != nil || !m.suggestionsEnabled || !persisted {
		t.Fatalf("command=%v enabled=%v persisted=%v", command != nil, m.suggestionsEnabled, persisted)
	}
}

func TestSettingsRememberToolApprovalsAppliesAfterRestart(t *testing.T) {
	bridge := NewBridge(context.Background(), tools.PermissionPrompt)
	defer bridge.Close()
	bridge.ConfigurePermissionPrompts("allow_once", false)
	persisted := false
	m := &model{
		width: 60, height: 16, bridge: bridge, settings: &settingsState{selected: 8},
		persistRemember: func(value bool) error { persisted = value; return nil },
	}
	updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command != nil || !m.rememberApprovals || !persisted || m.status != "settings updated; restart to apply" {
		t.Fatalf("command=%v remember=%v persisted=%v status=%q", command != nil, m.rememberApprovals, persisted, m.status)
	}
	done := make(chan error, 1)
	go func() { done <- bridge.Approve(context.Background(), "shell", "git status") }()
	request := (<-bridge.events).(approvalEvent)
	if len(request.options) != 3 {
		t.Fatalf("current session options=%#v", request.options)
	}
	for _, option := range request.options {
		if option.choice == approvalCommandAlways {
			t.Fatalf("restart-scoped choice applied to current session: %#v", request.options)
		}
	}
	request.reply <- approvalOnce
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestSettingsMultilineInputAppliesImmediately(t *testing.T) {
	m := &model{
		ctx: context.Background(), runner: &agent.Runner{},
		width: 60, height: 16, settings: &settingsState{selected: 10},
	}
	updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command != nil || !m.multiline || m.status != "settings updated" {
		t.Fatalf("command=%v multiline=%v status=%q", command != nil, m.multiline, m.status)
	}
	updated, command = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEsc}))
	m = updated.(*model)
	m.setInput("first")
	updated, command = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command != nil || string(m.input) != "first\n" || m.running {
		t.Fatalf("command=%v input=%q running=%v", command != nil, m.input, m.running)
	}
}

func TestSettingsQuestionTimeoutAppliesAfterRestart(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry(ws, tools.PromptApprover{Mode: tools.PermissionAuto})
	defer registry.Close()
	observer := &settingsQuestionObserver{}
	registry.SetUserQuestionObserver(observer)
	registry.ConfigureUserQuestions(true, time.Minute)
	persisted := true
	m := &model{
		width: 60, height: 16, runner: &agent.Runner{Tools: registry},
		questionTimeout: true, settings: &settingsState{selected: 9},
		persistQuestionTime: func(value bool) error { persisted = value; return nil },
	}
	updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command != nil || m.questionTimeout || persisted || m.status != "settings updated; restart to apply" {
		t.Fatalf("command=%v timeout=%v persisted=%v status=%q", command != nil, m.questionTimeout, persisted, m.status)
	}
	if _, err := registry.Execute(context.Background(), "ask_user_question", json.RawMessage(`{"questions":[{"question":"Continue?","options":[]}]}`)); err != nil {
		t.Fatal(err)
	}
	if !observer.hasDeadline {
		t.Fatal("restart-scoped timeout change applied to current session")
	}
}

func TestSettingsInvertScrollAppliesImmediately(t *testing.T) {
	persisted := false
	m := &model{
		width: 60, height: 16, scroll: 10, scrollLines: 5,
		settings:            &settingsState{selected: 11},
		persistInvertScroll: func(value bool) error { persisted = value; return nil },
	}
	updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command != nil || !m.invertScroll || !persisted {
		t.Fatalf("command=%v invert=%v persisted=%v", command != nil, m.invertScroll, persisted)
	}
	updated, command = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEsc}))
	m = updated.(*model)
	wheel := m.View().OnMouse(tea.MouseWheelMsg(tea.Mouse{Y: 1, Button: tea.MouseWheelUp}))
	if wheel == nil {
		t.Fatal("wheel event was ignored")
	}
	updated, _ = m.Update(wheel())
	m = updated.(*model)
	if m.scroll != 5 {
		t.Fatalf("inverted wheel-up scroll=%d", m.scroll)
	}
}

func TestSettingsTextSelectionAppliesImmediately(t *testing.T) {
	var persisted []string
	m := &model{
		width: 60, height: 16, selectionMode: selectionFlash,
		settings:         &settingsState{selected: 15},
		persistSelection: func(value string) error { persisted = append(persisted, value); return nil },
	}
	for _, want := range []textSelectionMode{selectionHold, selectionWord, selectionFlash} {
		updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
		m = updated.(*model)
		if command != nil || m.selectionMode != want {
			t.Fatalf("command=%v selection=%q want=%q", command != nil, m.selectionMode.canonical(), want.canonical())
		}
	}
	if strings.Join(persisted, ",") != "hold,word_select,flash" {
		t.Fatalf("persisted=%v", persisted)
	}
	m.selectionMode = selectionHold
	m.selection = &textSelection{nonce: 7}
	updated, _ := m.Update(selectionClearEvent{nonce: 7})
	m = updated.(*model)
	if m.selection == nil {
		t.Fatal("hold mode cleared the active selection")
	}
}

func TestSettingsScrollModeAppliesImmediately(t *testing.T) {
	var persisted []string
	m := &model{
		width: 60, height: 16, scrollLines: 3, scrollSpeed: 50,
		scrollInput:       scrollInput{mode: "auto", events: 2, serial: 7},
		settings:          &settingsState{selected: 13},
		persistScrollMode: func(value string) error { persisted = append(persisted, value); return nil },
	}
	serial := m.scrollInput.serial
	for _, want := range []string{"wheel", "trackpad", "auto"} {
		updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
		m = updated.(*model)
		serial++
		if command != nil || m.scrollInput.mode != want || m.scrollInput.events != 0 || m.scrollInput.serial != serial {
			t.Fatalf("command=%v scroll input=%#v want=%q", command != nil, m.scrollInput, want)
		}
	}
	if strings.Join(persisted, ",") != "wheel,trackpad,auto" {
		t.Fatalf("persisted=%v", persisted)
	}
	updated, _ := m.Update(mouseScrollFlushEvent{serial: 7, at: time.Unix(200, 0)})
	m = updated.(*model)
	if m.scroll != 0 {
		t.Fatalf("stale auto flush scrolled=%d", m.scroll)
	}

	m.scrollInput = scrollInput{mode: "auto"}
	m.settings = &settingsState{selected: 13}
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	updated, command := m.Update(mouseScrollEvent{direction: 1, at: time.Unix(100, 0), scale: true})
	m = updated.(*model)
	if command != nil || m.scroll != 3 {
		t.Fatalf("forced wheel command=%v scroll=%d", command != nil, m.scroll)
	}
}

func TestSettingsScrollNumberSteppers(t *testing.T) {
	defaults := &model{width: 60, height: 16, settings: &settingsState{selected: 13}}
	content := defaults.settingsContent()
	if !strings.Contains(content, "Scroll speed: 50") || !strings.Contains(content, "Scroll lines: 3") {
		t.Fatalf("default scroll settings=%q", content)
	}

	var speeds []uint8
	var lines []uint8
	m := &model{
		width: 60, height: 16, scrollSpeed: 50, scrollLines: 3,
		scrollInput:        scrollInput{mode: "auto", events: 2, serial: 7},
		settings:           &settingsState{selected: 12},
		persistScrollSpeed: func(value uint8) error { speeds = append(speeds, value); return nil },
		persistScrollLines: func(value uint8) error { lines = append(lines, value); return nil },
	}
	updated, _ := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	for _, key := range []tea.Key{
		{Code: tea.KeyRight},
		{Code: tea.KeyUp},
		{Code: tea.KeyEnter},
	} {
		updated, _ = m.Update(tea.KeyPressMsg(key))
		m = updated.(*model)
	}
	if m.scrollSpeed != 56 || m.scrollCarry != 0 || !reflect.DeepEqual(speeds, []uint8{56}) {
		t.Fatalf("speed=%d carry=%g persisted=%v", m.scrollSpeed, m.scrollCarry, speeds)
	}
	updated, _ = m.Update(mouseScrollFlushEvent{serial: 7, at: time.Unix(200, 0)})
	m = updated.(*model)
	if m.scroll != 0 || m.scrollInput.serial != 8 {
		t.Fatalf("stale speed flush scroll=%d input=%#v", m.scroll, m.scrollInput)
	}

	m.settings.selected = 14
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	for range 20 {
		updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyUp}))
		m = updated.(*model)
	}
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if m.scrollLines != 10 || !reflect.DeepEqual(lines, []uint8{10}) {
		t.Fatalf("lines=%d persisted=%v", m.scrollLines, lines)
	}
	updated, command := m.Update(mouseScrollEvent{direction: 1, at: time.Unix(100, 0), scale: true})
	m = updated.(*model)
	if command == nil {
		t.Fatal("auto scroll did not schedule a flush")
	}
	updated, _ = m.Update(mouseScrollFlushEvent{
		serial: m.scrollInput.serial,
		at:     m.scrollInput.last.Add(trackpadEventWindow),
	})
	m = updated.(*model)
	if m.scroll != 16 {
		t.Fatalf("live scroll=%d want=16", m.scroll)
	}

	m.settings.selected = 12
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyLeft}))
	m = updated.(*model)
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEsc}))
	m = updated.(*model)
	if m.scrollSpeed != 56 || m.settings.number != nil || len(speeds) != 1 || m.status != "settings" {
		t.Fatalf("cancel speed=%d persisted=%v settings=%#v status=%q", m.scrollSpeed, speeds, m.settings, m.status)
	}
}
