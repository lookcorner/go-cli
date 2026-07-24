package tui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
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
		if content := stripUIANSI(m.View().Content); !strings.Contains(content, "Settings") || !strings.Contains(content, "Timestamps: off") || !strings.Contains(content, "Mermaid rendering: auto") {
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
	if !m.showTimestamps || !m.showTimeline || !m.compactMode || !m.vimMode || !m.defaultMinimal || strings.Join(booleans, ",") != "timestamps,timeline,compact,vim" || strings.Join(screenModes, ",") != "minimal" {
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

	m.settings.selected = 5
	m.mermaidMode = "auto"
	m.persistMermaid = func(string) error { return errors.New("read only") }
	updated, command = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command != nil || m.mermaidMode != "auto" || m.settings.err != "read only" {
		t.Fatalf("command=%v Mermaid=%q err=%q", command != nil, m.mermaidMode, m.settings.err)
	}

	m.settings.selected = 6
	m.persistTheme = func(string) error { return errors.New("read only") }
	updated, command = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command != nil || m.themeName != "auto" || m.settings.err != "read only" {
		t.Fatalf("command=%v theme=%q err=%q", command != nil, m.themeName, m.settings.err)
	}
}
