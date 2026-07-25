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
	var autoDarkThemes []string
	var autoLightThemes []string
	var hunkTrackerModes []string
	var defaultPermissions []string
	var cancelPolicies []string
	m := &model{
		width: 70, height: 18, themeName: "groknight", theme: paletteFor("groknight"), mermaidMode: "auto",
		autoDarkTheme: "groknight", autoLightTheme: "grokday", hunkTrackerMode: "agent_only",
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
		undoHint: contextualHintState{
			persist: func(value bool) error {
				booleans = append(booleans, "undo-hint")
				return nil
			},
		},
		planModeHint: contextualHintState{
			persist: func(value bool) error {
				booleans = append(booleans, "plan-mode-hint")
				return nil
			},
		},
		sendNowHint: contextualHintState{
			persist: func(value bool) error {
				booleans = append(booleans, "send-now-hint")
				return nil
			},
		},
		smallScreenHint: smallScreenHintState{
			persist: func(value bool) error {
				booleans = append(booleans, "small-screen-hint")
				return nil
			},
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
		persistAutoDark: func(value string) error {
			autoDarkThemes = append(autoDarkThemes, value)
			return nil
		},
		persistAutoLight: func(value string) error {
			autoLightThemes = append(autoLightThemes, value)
			return nil
		},
		persistHunkTracker: func(value string) error {
			hunkTrackerModes = append(hunkTrackerModes, value)
			return nil
		},
		defaultPermission: "always_allow_all_sessions",
		persistPermission: func(value string) error {
			defaultPermissions = append(defaultPermissions, value)
			return nil
		},
		persistCancelSubs: func(value string) error {
			cancelPolicies = append(cancelPolicies, value)
			return nil
		},
	}
	for index := 0; index < settingsCount; index++ {
		if index == 22 || index == 23 {
			continue
		}
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
		if index == 8 || index == 9 || index == 17 {
			wantStatus = "settings updated; restart to apply"
		}
		if command != nil || m.settings.err != "" || m.status != wantStatus {
			t.Fatalf("index=%d command=%v err=%q status=%q", index, command != nil, m.settings.err, m.status)
		}
	}
	if !m.showTimestamps || !m.showTimeline || !m.compactMode || !m.vimMode || !m.defaultMinimal || !m.groupToolVerbs || !m.collapsedEditBlocks || !m.suggestionsEnabled || !m.rememberApprovals || !m.questionTimeout || !m.multiline || !m.invertScroll || !m.undoHint.enabled || !m.planModeHint.enabled || !m.sendNowHint.enabled || !m.smallScreenHint.enabled ||
		strings.Join(booleans, ",") != "timestamps,timeline,compact,vim,group,edits,suggestions,remember,question-timeout,invert-scroll,undo-hint,plan-mode-hint,send-now-hint,small-screen-hint" || strings.Join(screenModes, ",") != "minimal" {
		t.Fatalf("timestamps=%v timeline=%v compact=%v vim=%v persisted=%v", m.showTimestamps, m.showTimeline, m.compactMode, m.vimMode, booleans)
	}
	if m.themeName != "grokday" || m.theme.name != "grokday" || strings.Join(themes, ",") != "grokday" {
		t.Fatalf("theme=%q palette=%q persisted=%v", m.themeName, m.theme.name, themes)
	}
	if m.autoDarkTheme != "grokday" || m.autoLightTheme != "tokyonight" || strings.Join(autoDarkThemes, ",") != "grokday" || strings.Join(autoLightThemes, ",") != "tokyonight" {
		t.Fatalf("auto themes=%q/%q persisted=%v/%v", m.autoDarkTheme, m.autoLightTheme, autoDarkThemes, autoLightThemes)
	}
	if m.mermaidMode != "on" || strings.Join(mermaidModes, ",") != "on" {
		t.Fatalf("Mermaid=%q persisted=%v", m.mermaidMode, mermaidModes)
	}
	if m.hunkTrackerMode != "all_dirty" || strings.Join(hunkTrackerModes, ",") != "all_dirty" {
		t.Fatalf("hunk tracker=%q persisted=%v", m.hunkTrackerMode, hunkTrackerModes)
	}
	if m.cancelSubagents != "always_stop" || strings.Join(cancelPolicies, ",") != "always_stop" {
		t.Fatalf("cancel policy=%q persisted=%v", m.cancelSubagents, cancelPolicies)
	}
	if m.defaultPermission != "allow_command_always" || strings.Join(defaultPermissions, ",") != "allow_command_always" {
		t.Fatalf("default permission=%q persisted=%v", m.defaultPermission, defaultPermissions)
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

func TestSettingsContextualUndoHintRollsBackPersistenceFailure(t *testing.T) {
	m := &model{
		undoHint: contextualHintState{
			enabled: true,
			persist: func(bool) error {
				return errors.New("read only")
			},
		},
		settings: &settingsState{selected: 25},
	}
	updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command != nil || !m.undoHint.enabled || m.settings.err != "read only" || m.status != "setting update failed" {
		t.Fatalf("command=%v enabled=%v err=%q status=%q", command != nil, m.undoHint.enabled, m.settings.err, m.status)
	}
}

func TestSettingsContextualSendNowHintRollsBackPersistenceFailure(t *testing.T) {
	m := &model{
		sendNowHint: contextualHintState{
			enabled: true,
			persist: func(bool) error {
				return errors.New("read only")
			},
		},
		settings: &settingsState{selected: 27},
	}
	updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command != nil || !m.sendNowHint.enabled || m.settings.err != "read only" || m.status != "setting update failed" {
		t.Fatalf("command=%v enabled=%v err=%q status=%q", command != nil, m.sendNowHint.enabled, m.settings.err, m.status)
	}
}

func TestSettingsContextualPlanModeHintRollsBackPersistenceFailure(t *testing.T) {
	m := &model{
		planModeHint: contextualHintState{
			enabled: true,
			persist: func(bool) error {
				return errors.New("read only")
			},
		},
		settings: &settingsState{selected: 26},
	}
	updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command != nil || !m.planModeHint.enabled || m.settings.err != "read only" || m.status != "setting update failed" {
		t.Fatalf("command=%v enabled=%v err=%q status=%q", command != nil, m.planModeHint.enabled, m.settings.err, m.status)
	}
}

func TestSettingsContextualSmallScreenHintRollsBackPersistenceFailure(t *testing.T) {
	m := &model{
		smallScreenHint: smallScreenHintState{
			enabled: true,
			persist: func(bool) error {
				return errors.New("read only")
			},
		},
		settings: &settingsState{selected: 28},
	}
	updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command != nil || !m.smallScreenHint.enabled || m.settings.err != "read only" || m.status != "setting update failed" {
		t.Fatalf("command=%v enabled=%v err=%q status=%q", command != nil, m.smallScreenHint.enabled, m.settings.err, m.status)
	}
}

func TestSettingsPanelRollsBackFailedPersistence(t *testing.T) {
	t.Setenv("TERM_BACKGROUND", "dark")
	t.Setenv("COLORFGBG", "")
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

	m.settings.selected = 20
	m.persistTheme = func(string) error { return errors.New("read only") }
	updated, command = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command != nil || m.themeName != "auto" || m.settings.err != "read only" {
		t.Fatalf("command=%v theme=%q err=%q", command != nil, m.themeName, m.settings.err)
	}

	m.settings.selected = 21
	m.defaultPermission = "always_allow_all_sessions"
	m.persistPermission = func(string) error { return errors.New("read only") }
	updated, command = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command != nil || m.defaultPermission != "always_allow_all_sessions" || m.settings.err != "read only" {
		t.Fatalf("command=%v default permission=%q err=%q", command != nil, m.defaultPermission, m.settings.err)
	}

	m.settings.selected = 17
	m.hunkTrackerMode = "agent_only"
	m.persistHunkTracker = func(string) error { return errors.New("read only") }
	updated, command = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command != nil || m.hunkTrackerMode != "agent_only" || m.settings.err != "read only" {
		t.Fatalf("command=%v hunk tracker=%q err=%q", command != nil, m.hunkTrackerMode, m.settings.err)
	}

	m.settings.selected = 18
	m.autoDarkTheme = "groknight"
	m.themeName = "auto"
	m.theme = paletteForAuto("auto", "groknight", "grokday")
	m.persistAutoDark = func(string) error { return errors.New("read only") }
	updated, command = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command != nil || m.autoDarkTheme != "groknight" || m.theme.name != "groknight" || m.settings.err != "read only" {
		t.Fatalf("command=%v auto dark=%q palette=%q err=%q", command != nil, m.autoDarkTheme, m.theme.name, m.settings.err)
	}

	m.settings.selected = 19
	m.autoLightTheme = "grokday"
	m.persistAutoLight = func(string) error { return errors.New("read only") }
	updated, command = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command != nil || m.autoLightTheme != "grokday" || m.theme.name != "groknight" || m.settings.err != "read only" {
		t.Fatalf("command=%v auto light=%q palette=%q err=%q", command != nil, m.autoLightTheme, m.theme.name, m.settings.err)
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
	grouped, messages, expands, folds, err := sessionDisplayTranscript(path, "", false, true)
	if err != nil {
		t.Fatal(err)
	}
	persisted := true
	m := &model{
		width: 80, height: 20, runner: &agent.Runner{SessionPath: path},
		groupToolVerbs: true, settings: &settingsState{selected: 5},
		persistGroupTools: func(value bool) error { persisted = value; return nil },
	}
	m.replaceDisplayTranscript(grouped, messages, expands, folds)
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
	expanded, messages, expands, folds, err := sessionDisplayTranscript(path, "", false, true)
	if err != nil {
		t.Fatal(err)
	}
	persisted := false
	m := &model{
		width: 80, height: 20, runner: &agent.Runner{SessionPath: path},
		groupToolVerbs: true, settings: &settingsState{selected: 6},
		persistEditBlocks: func(value bool) error { persisted = value; return nil },
	}
	m.replaceDisplayTranscript(expanded, messages, expands, folds)
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

func TestSettingsDefaultSelectedPermissionAppliesToNextPrompt(t *testing.T) {
	bridge := NewBridge(context.Background(), tools.PermissionPrompt)
	defer bridge.Close()
	bridge.ConfigurePermissionPrompts("always_allow_all_sessions", true)
	var persisted string
	m := &model{
		width: 60, height: 16, bridge: bridge,
		defaultPermission: "always_allow_all_sessions",
		settings:          &settingsState{selected: 21},
		persistPermission: func(value string) error {
			persisted = value
			bridge.SetDefaultSelectedPermission(value)
			return nil
		},
	}
	updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command != nil || persisted != "allow_command_always" || m.status != "settings updated" {
		t.Fatalf("command=%v persisted=%q status=%q", command != nil, persisted, m.status)
	}
	done := make(chan error, 1)
	go func() { done <- bridge.Approve(context.Background(), "shell", "git status") }()
	request := (<-bridge.events).(approvalEvent)
	if request.options[request.selected].choice != approvalCommandAlways {
		t.Fatalf("selected=%d options=%#v", request.selected, request.options)
	}
	request.reply <- approvalOnce
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestSettingsDefaultModelReusesModelPicker(t *testing.T) {
	m, _ := modelTUIFixture(t)
	m.defaultModelID = "plain"
	var persisted []string
	m.runner.SetDefaultModel = func(id string) error {
		persisted = append(persisted, id)
		return nil
	}
	settings := &settingsState{selected: 22}
	m.settings = settings

	updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command != nil || m.settings != nil || m.modelSelect == nil || m.modelSelect.settings != settings ||
		len(m.modelSelect.models) != 3 || m.modelSelect.selected != 1 ||
		!strings.Contains(stripUIANSI(m.View().Content), "Default model") {
		t.Fatalf("command=%v settings=%#v picker=%#v view=%q", command != nil, m.settings, m.modelSelect, stripUIANSI(m.View().Content))
	}
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown}))
	m = updated.(*model)
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if m.modelSelect != nil || m.settings != settings || m.defaultModelID != "reasoning" ||
		m.runner.ModelID != "reasoning" || strings.Join(persisted, ",") != "reasoning" ||
		!strings.Contains(m.settingsContent(), "Default model: Reasoning X") {
		t.Fatalf("settings=%#v picker=%#v default=%q model=%q persisted=%v content=%q", m.settings, m.modelSelect, m.defaultModelID, m.runner.ModelID, persisted, m.settingsContent())
	}
}

func TestSettingsDefaultModelClearAndCancelReturnToSettings(t *testing.T) {
	m, _ := modelTUIFixture(t)
	m.defaultModelID = "plain"
	var persisted []string
	m.runner.SetDefaultModel = func(id string) error {
		persisted = append(persisted, id)
		return nil
	}
	settings := &settingsState{selected: 22}
	m.settings = settings

	updated, _ := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyUp}))
	m = updated.(*model)
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if m.settings != settings || m.modelSelect != nil || m.defaultModelID != "" || m.runner.ModelID != "plain" ||
		len(persisted) != 1 || persisted[0] != "" || m.status != "default model override cleared" {
		t.Fatalf("settings=%#v picker=%#v default=%q model=%q persisted=%q status=%q", m.settings, m.modelSelect, m.defaultModelID, m.runner.ModelID, persisted, m.status)
	}

	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEsc}))
	m = updated.(*model)
	if m.settings != settings || m.modelSelect != nil || m.status != "settings" {
		t.Fatalf("cancel settings=%#v picker=%#v status=%q", m.settings, m.modelSelect, m.status)
	}
}

func TestSettingsDefaultModelFailureDoesNotLeavePartialDefault(t *testing.T) {
	m, _ := modelTUIFixture(t)
	m.defaultModelID = "plain"
	settings := &settingsState{selected: 22}
	m.settings = settings
	m.runner.SetDefaultModel = func(string) error { return errors.New("read only") }

	updated, _ := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown}))
	m = updated.(*model)
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if m.modelSelect == nil || m.modelSelect.phase != modelSelectError || m.defaultModelID != "plain" || m.runner.ModelID != "plain" ||
		!strings.Contains(m.modelSelect.err, "persist default model") {
		t.Fatalf("picker=%#v default=%q model=%q", m.modelSelect, m.defaultModelID, m.runner.ModelID)
	}
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEsc}))
	m = updated.(*model)
	if m.settings != settings || m.modelSelect != nil {
		t.Fatalf("error close settings=%#v picker=%#v", m.settings, m.modelSelect)
	}
}

func TestSettingsDefaultModelSwitchFailureRestoresPreviousDefault(t *testing.T) {
	m, _ := modelTUIFixture(t)
	m.defaultModelID = "plain"
	var persisted []string
	m.runner.SetDefaultModel = func(id string) error {
		persisted = append(persisted, id)
		return nil
	}
	m.runner.ResolveModel = func(string) (agent.ModelRuntime, error) {
		return agent.ModelRuntime{}, errors.New("model unavailable")
	}
	settings := &settingsState{selected: 22}
	m.settings = settings

	updated, _ := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown}))
	m = updated.(*model)
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if m.modelSelect == nil || m.modelSelect.phase != modelSelectError || m.defaultModelID != "plain" ||
		m.runner.ModelID != "plain" || strings.Join(persisted, ",") != "reasoning,plain" ||
		!strings.Contains(m.modelSelect.err, "model unavailable") {
		t.Fatalf("picker=%#v default=%q model=%q persisted=%v", m.modelSelect, m.defaultModelID, m.runner.ModelID, persisted)
	}
}

func TestSettingsForkSecondaryModelReusesCatalogWithoutSwitching(t *testing.T) {
	m, _ := modelTUIFixture(t)
	m.forkSecondaryModel = "plain"
	var persisted []string
	m.persistForkModel = func(id string) error {
		persisted = append(persisted, id)
		return nil
	}
	settings := &settingsState{selected: 23}
	m.settings = settings

	updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command != nil || m.settings != nil || m.modelSelect == nil || m.modelSelect.setting != "fork" ||
		len(m.modelSelect.models) != 3 || m.modelSelect.selected != 1 ||
		!strings.Contains(stripUIANSI(m.View().Content), "Fork secondary model") {
		t.Fatalf("command=%v settings=%#v picker=%#v view=%q", command != nil, m.settings, m.modelSelect, stripUIANSI(m.View().Content))
	}
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown}))
	m = updated.(*model)
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if m.modelSelect != nil || m.settings != settings || m.forkSecondaryModel != "reasoning" ||
		m.runner.ModelID != "plain" || strings.Join(persisted, ",") != "reasoning" ||
		!strings.Contains(m.settingsContent(), "Fork secondary model: Reasoning X") {
		t.Fatalf("settings=%#v picker=%#v fork=%q model=%q persisted=%v content=%q", m.settings, m.modelSelect, m.forkSecondaryModel, m.runner.ModelID, persisted, m.settingsContent())
	}
}

func TestSettingsForkSecondaryModelClearsAndRollsBack(t *testing.T) {
	m, _ := modelTUIFixture(t)
	m.forkSecondaryModel = "plain"
	var persisted []string
	m.persistForkModel = func(id string) error {
		persisted = append(persisted, id)
		return nil
	}
	settings := &settingsState{selected: 23}
	m.settings = settings

	updated, _ := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyUp}))
	m = updated.(*model)
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if m.settings != settings || m.modelSelect != nil || m.forkSecondaryModel != "" ||
		len(persisted) != 1 || persisted[0] != "" || m.status != "fork secondary model override cleared" {
		t.Fatalf("settings=%#v picker=%#v fork=%q persisted=%q status=%q", m.settings, m.modelSelect, m.forkSecondaryModel, persisted, m.status)
	}

	m.forkSecondaryModel = "plain"
	m.persistForkModel = func(string) error { return errors.New("read only") }
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown}))
	m = updated.(*model)
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if m.modelSelect == nil || m.modelSelect.phase != modelSelectError || m.forkSecondaryModel != "plain" ||
		!strings.Contains(m.modelSelect.err, "persist fork secondary model") {
		t.Fatalf("picker=%#v fork=%q", m.modelSelect, m.forkSecondaryModel)
	}
}

func TestNextDefaultSelectedPermissionCyclesAllChoices(t *testing.T) {
	value := "always_allow_all_sessions"
	want := []string{"allow_command_always", "allow_once", "reject", "always_allow_all_sessions"}
	for index, expected := range want {
		value = nextDefaultSelectedPermission(value)
		if value != expected {
			t.Fatalf("step=%d value=%q want=%q", index, value, expected)
		}
	}
	if value := nextDefaultSelectedPermission("invalid"); value != "always_allow_all_sessions" {
		t.Fatalf("invalid value normalized to %q", value)
	}
}

func TestSettingsCancelSubagentsPolicyCyclesAndRollsBack(t *testing.T) {
	var persisted []string
	m := &model{
		width: 60, height: 16,
		settings:        &settingsState{selected: 24},
		cancelSubagents: "ask",
		persistCancelSubs: func(value string) error {
			persisted = append(persisted, value)
			return nil
		},
	}
	for _, want := range []string{"always_stop", "always_continue", "ask"} {
		updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
		m = updated.(*model)
		if command != nil || m.cancelSubagents != want || m.settings.err != "" {
			t.Fatalf("want=%q policy=%q err=%q command=%v", want, m.cancelSubagents, m.settings.err, command != nil)
		}
	}
	if strings.Join(persisted, ",") != "always_stop,always_continue,ask" {
		t.Fatalf("persisted=%v", persisted)
	}

	m.persistCancelSubs = func(string) error { return errors.New("read only") }
	updated, _ := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if m.cancelSubagents != "ask" || m.settings.err != "read only" || m.status != "setting update failed" {
		t.Fatalf("policy=%q err=%q status=%q", m.cancelSubagents, m.settings.err, m.status)
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

func TestSettingsAutomaticThemesApplyOnlyWhenLive(t *testing.T) {
	t.Setenv("COLORFGBG", "")
	var persisted []string

	t.Setenv("TERM_BACKGROUND", "dark")
	m := &model{
		width: 60, height: 16, themeName: "auto", autoDarkTheme: "groknight", autoLightTheme: "grokday",
		theme:            paletteForAuto("auto", "groknight", "grokday"),
		settings:         &settingsState{selected: 18},
		persistAutoDark:  func(value string) error { persisted = append(persisted, "dark:"+value); return nil },
		persistAutoLight: func(value string) error { persisted = append(persisted, "light:"+value); return nil },
	}
	updated, _ := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if m.autoDarkTheme != "grokday" || m.theme.name != "grokday" {
		t.Fatalf("live dark mapping=%q palette=%q", m.autoDarkTheme, m.theme.name)
	}

	m.settings.selected = 19
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if m.autoLightTheme != "tokyonight" || m.theme.name != "grokday" {
		t.Fatalf("inactive light mapping=%q palette=%q", m.autoLightTheme, m.theme.name)
	}

	m.themeName = "oscura-midnight"
	m.theme = paletteFor("oscura-midnight")
	m.settings.selected = 18
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if m.autoDarkTheme != "tokyonight" || m.theme.name != "oscura-midnight" {
		t.Fatalf("concrete theme mapping=%q palette=%q", m.autoDarkTheme, m.theme.name)
	}
	if strings.Join(persisted, ",") != "dark:grokday,light:tokyonight,dark:tokyonight" {
		t.Fatalf("persisted=%v", persisted)
	}
}

func TestSettingsAutomaticThemesUseDefaultsWhenMappingsAreEmpty(t *testing.T) {
	t.Setenv("TERM_BACKGROUND", "dark")
	t.Setenv("COLORFGBG", "")

	var persisted string
	m := &model{
		width: 60, height: 16, themeName: "auto",
		theme:           paletteForAuto("auto", "", ""),
		settings:        &settingsState{selected: 18},
		persistAutoDark: func(value string) error { persisted = value; return nil },
	}
	if content := m.settingsContent(); !strings.Contains(content, "Auto dark theme: groknight") || !strings.Contains(content, "Auto light theme: grokday") {
		t.Fatalf("settings content=%q", content)
	}

	updated, _ := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if m.autoDarkTheme != "grokday" || m.theme.name != "grokday" || persisted != "grokday" {
		t.Fatalf("auto dark=%q palette=%q persisted=%q", m.autoDarkTheme, m.theme.name, persisted)
	}
}

func TestSettingsHunkTrackerUsesDefaultAndCycles(t *testing.T) {
	var persisted []string
	m := &model{
		width: 60, height: 16,
		settings:           &settingsState{selected: 17},
		persistHunkTracker: func(value string) error { persisted = append(persisted, value); return nil },
	}
	if content := m.settingsContent(); !strings.Contains(content, "Hunk tracker (restart): agent_only") {
		t.Fatalf("settings content=%q", content)
	}

	for range 3 {
		updated, _ := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
		m = updated.(*model)
		if m.status != "settings updated; restart to apply" {
			t.Fatalf("status=%q", m.status)
		}
	}
	if m.hunkTrackerMode != "agent_only" || strings.Join(persisted, ",") != "all_dirty,off,agent_only" {
		t.Fatalf("hunk tracker=%q persisted=%v", m.hunkTrackerMode, persisted)
	}
}

func TestSettingsVoiceLanguageIsCapabilityGatedAndRollsBack(t *testing.T) {
	withoutVoice := &model{width: 60, height: 16, settings: &settingsState{}}
	if withoutVoice.settingsCount() != settingsCount || strings.Contains(withoutVoice.settingsContent(), "Voice language:") {
		t.Fatalf("voice setting shown without capability: count=%d content=%q", withoutVoice.settingsCount(), withoutVoice.settingsContent())
	}

	var persisted []string
	withVoice := &model{
		width: 60, height: 16,
		voiceClient:          fakeVoiceStarter{},
		voiceLanguage:        "en",
		settings:             &settingsState{selected: settingsCount},
		persistVoiceLanguage: func(value string) error { persisted = append(persisted, value); return nil },
	}
	if withVoice.settingsCount() != settingsCount+1 || !strings.Contains(withVoice.settingsContent(), "Voice language: en") {
		t.Fatalf("voice setting missing: count=%d content=%q", withVoice.settingsCount(), withVoice.settingsContent())
	}
	updated, command := withVoice.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	withVoice = updated.(*model)
	if command != nil || withVoice.voiceLanguage != "auto" || strings.Join(persisted, ",") != "auto" || withVoice.status != "settings updated" {
		t.Fatalf("voice language=%q persisted=%v status=%q command=%v", withVoice.voiceLanguage, persisted, withVoice.status, command != nil)
	}

	withVoice.persistVoiceLanguage = func(string) error { return errors.New("read only") }
	updated, command = withVoice.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	withVoice = updated.(*model)
	if command != nil || withVoice.voiceLanguage != "auto" || withVoice.settings.err != "read only" || withVoice.status != "setting update failed" {
		t.Fatalf("rollback language=%q err=%q status=%q command=%v", withVoice.voiceLanguage, withVoice.settings.err, withVoice.status, command != nil)
	}
}

func TestSettingsPlanModeIsCapabilityGatedAndUsesRegistry(t *testing.T) {
	withoutPlan := &model{width: 60, height: 16, settings: &settingsState{}}
	if withoutPlan.settingsCount() != settingsCount || strings.Contains(withoutPlan.settingsContent(), "Plan mode:") {
		t.Fatalf("plan setting shown without capability: count=%d content=%q", withoutPlan.settingsCount(), withoutPlan.settingsContent())
	}

	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry(ws, tools.PromptApprover{Mode: tools.PermissionAuto})
	defer registry.Close()
	if err := registry.ConfigurePlanMode(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	withPlan := &model{
		width: 60, height: 16,
		runner:   &agent.Runner{Tools: registry},
		settings: &settingsState{selected: settingsCount},
	}
	if withPlan.settingsCount() != settingsCount+1 || !strings.Contains(withPlan.settingsContent(), "Plan mode: off") {
		t.Fatalf("plan setting missing: count=%d content=%q", withPlan.settingsCount(), withPlan.settingsContent())
	}
	updated, command := withPlan.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	withPlan = updated.(*model)
	if command != nil || !withPlan.planMode || !registry.PlanModeActive() || withPlan.status != "settings updated" {
		t.Fatalf("enabled plan=%v active=%v status=%q command=%v", withPlan.planMode, registry.PlanModeActive(), withPlan.status, command != nil)
	}
	updated, command = withPlan.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	withPlan = updated.(*model)
	if command != nil || withPlan.planMode || registry.PlanModeActive() || withPlan.status != "settings updated" {
		t.Fatalf("disabled plan=%v active=%v status=%q command=%v", withPlan.planMode, registry.PlanModeActive(), withPlan.status, command != nil)
	}
}

func TestSettingsDynamicRowsKeepPlanBeforeVoice(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry(ws, tools.PromptApprover{Mode: tools.PermissionAuto})
	defer registry.Close()
	if err := registry.ConfigurePlanMode(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	var persisted string
	m := &model{
		width: 60, height: 16,
		runner:               &agent.Runner{Tools: registry},
		voiceClient:          fakeVoiceStarter{},
		voiceLanguage:        "en",
		settings:             &settingsState{selected: settingsCount + 1},
		persistVoiceLanguage: func(value string) error { persisted = value; return nil },
	}
	if m.settingsCount() != settingsCount+2 {
		t.Fatalf("settings count=%d", m.settingsCount())
	}
	content := m.settingsContent()
	if plan, voice := strings.Index(content, "Plan mode: off"), strings.Index(content, "Voice language: en"); plan < 0 || voice < plan {
		t.Fatalf("dynamic row order=%q", content)
	}
	updated, _ := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if m.planMode || registry.PlanModeActive() || m.voiceLanguage != "auto" || persisted != "auto" {
		t.Fatalf("plan=%v active=%v voice=%q persisted=%q", m.planMode, registry.PlanModeActive(), m.voiceLanguage, persisted)
	}
}
