package tui

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/session"
)

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
	m := &model{
		width: 70, height: 18, themeName: "groknight", theme: paletteFor("groknight"), mermaidMode: "auto", settings: &settingsState{},
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
		if command != nil || m.settings.err != "" || m.status != "settings updated" {
			t.Fatalf("index=%d command=%v err=%q status=%q", index, command != nil, m.settings.err, m.status)
		}
	}
	if !m.showTimestamps || !m.showTimeline || !m.compactMode || !m.vimMode || !m.defaultMinimal || !m.groupToolVerbs || !m.collapsedEditBlocks ||
		strings.Join(booleans, ",") != "timestamps,timeline,compact,vim,group,edits" || strings.Join(screenModes, ",") != "minimal" {
		t.Fatalf("timestamps=%v timeline=%v compact=%v vim=%v persisted=%v", m.showTimestamps, m.showTimeline, m.compactMode, m.vimMode, booleans)
	}
	if m.themeName != "grokday" || m.theme.name != "grokday" || strings.Join(themes, ",") != "grokday" {
		t.Fatalf("theme=%q palette=%q persisted=%v", m.themeName, m.theme.name, themes)
	}
	if m.mermaidMode != "on" || strings.Join(mermaidModes, ",") != "on" {
		t.Fatalf("Mermaid=%q persisted=%v", m.mermaidMode, mermaidModes)
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

	m.settings.selected = 7
	m.mermaidMode = "auto"
	m.persistMermaid = func(string) error { return errors.New("read only") }
	updated, command = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command != nil || m.mermaidMode != "auto" || m.settings.err != "read only" {
		t.Fatalf("command=%v Mermaid=%q err=%q", command != nil, m.mermaidMode, m.settings.err)
	}

	m.settings.selected = 8
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
