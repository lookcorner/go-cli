package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	uitheme "github.com/lookcorner/go-cli/internal/theme"
)

type settingsState struct {
	selected int
	err      string
}

const settingsCount = 15

func (m *model) openSettings() {
	m.settings = &settingsState{}
	m.scroll = 0
	m.status = "settings"
}

func (m *model) handleSettingsKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.Key()
	if key.Code == tea.KeyEsc {
		m.settings = nil
		m.status = "ready"
		return m, nil
	}
	if key.Code == tea.KeyUp || key.Text == "k" {
		m.settings.selected = max(0, m.settings.selected-1)
		return m, nil
	}
	if key.Code == tea.KeyDown || key.Text == "j" {
		m.settings.selected = min(settingsCount-1, m.settings.selected+1)
		return m, nil
	}
	if key.Code != tea.KeyEnter && key.Code != tea.KeySpace && key.Text != " " {
		return m, nil
	}
	m.applySetting(m.settings.selected)
	return m, nil
}

func (m *model) applySetting(selected int) {
	state := m.settings
	state.err = ""
	restartRequired := false
	switch selected {
	case 0:
		previous := m.showTimestamps
		m.showTimestamps = !previous
		if m.persistTimestamps != nil {
			state.err = persistSetting(m.persistTimestamps(m.showTimestamps), func() { m.showTimestamps = previous })
		}
	case 1:
		previous := m.showTimeline
		m.showTimeline = !previous
		m.timelineHover = nil
		if m.persistTimeline != nil {
			state.err = persistSetting(m.persistTimeline(m.showTimeline), func() { m.showTimeline = previous })
		}
	case 2:
		previous := m.compactMode
		m.compactMode = !previous
		if m.persistCompactMode != nil {
			state.err = persistSetting(m.persistCompactMode(m.compactMode), func() { m.compactMode = previous })
		}
	case 3:
		previous := m.vimMode
		m.vimMode = !previous
		if m.persistVimMode != nil {
			state.err = persistSetting(m.persistVimMode(m.vimMode), func() { m.vimMode = previous })
		}
	case 4:
		previous := m.defaultMinimal
		m.defaultMinimal = !previous
		if m.persistScreenMode != nil {
			mode := "fullscreen"
			if m.defaultMinimal {
				mode = "minimal"
			}
			state.err = persistSetting(m.persistScreenMode(mode), func() { m.defaultMinimal = previous })
		}
	case 5:
		previous := m.groupToolVerbs
		m.groupToolVerbs = !previous
		if m.persistGroupTools != nil {
			state.err = persistSetting(m.persistGroupTools(m.groupToolVerbs), func() { m.groupToolVerbs = previous })
		}
		if state.err == "" && !m.minimal {
			m.refreshToolDisplay(m.collapsedEditBlocks, previous)
		}
	case 6:
		previous := m.collapsedEditBlocks
		m.collapsedEditBlocks = !previous
		if m.persistEditBlocks != nil {
			state.err = persistSetting(m.persistEditBlocks(m.collapsedEditBlocks), func() { m.collapsedEditBlocks = previous })
		}
		if state.err == "" && !m.minimal {
			m.refreshToolDisplay(previous, m.groupToolVerbs)
		}
	case 7:
		previous := m.suggestionsEnabled
		m.suggestionsEnabled = !previous
		if m.persistSuggestions != nil {
			state.err = persistSetting(m.persistSuggestions(m.suggestionsEnabled), func() { m.suggestionsEnabled = previous })
		}
		if state.err == "" && !m.suggestionsEnabled {
			m.clearPromptSuggestion()
		}
	case 8:
		previous := m.rememberApprovals
		m.rememberApprovals = !previous
		if m.persistRemember != nil {
			state.err = persistSetting(m.persistRemember(m.rememberApprovals), func() { m.rememberApprovals = previous })
		}
		restartRequired = state.err == ""
	case 9:
		previous := m.questionTimeout
		m.questionTimeout = !previous
		if m.persistQuestionTime != nil {
			state.err = persistSetting(m.persistQuestionTime(m.questionTimeout), func() { m.questionTimeout = previous })
		}
		restartRequired = state.err == ""
	case 10:
		m.toggleMultiline()
	case 11:
		previous := m.invertScroll
		m.invertScroll = !previous
		if m.persistInvertScroll != nil {
			state.err = persistSetting(m.persistInvertScroll(m.invertScroll), func() { m.invertScroll = previous })
		}
	case 12:
		previous := m.selectionMode
		m.selectionMode = previous.next()
		if m.persistSelection != nil {
			state.err = persistSetting(m.persistSelection(m.selectionMode.canonical()), func() { m.selectionMode = previous })
		}
	case 13:
		previous := m.mermaidMode
		switch m.mermaidMode {
		case "auto":
			m.mermaidMode = "on"
		case "on":
			m.mermaidMode = "off"
		default:
			m.mermaidMode = "auto"
		}
		if m.persistMermaid != nil {
			state.err = persistSetting(m.persistMermaid(m.mermaidMode), func() { m.mermaidMode = previous })
		}
	case 14:
		previousName, previousTheme := m.themeName, m.theme
		m.themeName = nextTheme(m.themeName)
		m.theme = paletteForAuto(m.themeName, m.autoDarkTheme, m.autoLightTheme)
		if m.persistTheme != nil {
			state.err = persistSetting(m.persistTheme(m.themeName), func() { m.themeName, m.theme = previousName, previousTheme })
		}
	}
	if state.err != "" {
		m.status = "setting update failed"
	} else if restartRequired {
		m.status = "settings updated; restart to apply"
	} else {
		m.status = "settings updated"
	}
}

func persistSetting(err error, rollback func()) string {
	if err == nil {
		return ""
	}
	rollback()
	return err.Error()
}

func nextTheme(current string) string {
	names := append([]string{"auto"}, uitheme.Names[:]...)
	for index, name := range names {
		if name == current {
			return names[(index+1)%len(names)]
		}
	}
	return names[0]
}

func (m *model) settingsContent() string {
	if m.settings == nil {
		return ""
	}
	mermaidMode := m.mermaidMode
	if mermaidMode == "" {
		mermaidMode = "auto"
	}
	lines := []string{
		settingLine("Timestamps", m.showTimestamps),
		settingLine("Timeline", m.showTimeline),
		settingLine("Compact mode", m.compactMode),
		settingLine("Vim navigation", m.vimMode),
		settingLine("Minimal by default", m.defaultMinimal),
		settingLine("Group tool verbs", m.groupToolVerbs),
		settingLine("Collapsed edit blocks", m.collapsedEditBlocks),
		settingLine("Prompt suggestions", m.suggestionsEnabled),
		settingLine("Remember tool approvals (restart)", m.rememberApprovals),
		settingLine("Ask-question timeout (restart)", m.questionTimeout),
		settingLine("Multiline input", m.multiline),
		settingLine("Invert scroll", m.invertScroll),
		fmt.Sprintf("Text selection: %s", m.selectionMode.canonical()),
		fmt.Sprintf("Mermaid rendering: %s", mermaidMode),
		fmt.Sprintf("Theme: %s", m.themeName),
	}
	content := "# Settings\n\n" + selectedWindow(lines, m.settings.selected, max(m.contentHeight()-3, 1))
	if m.settings.err != "" {
		content += "\n\n**Error:** " + strings.ReplaceAll(sanitizeTerminalText(m.settings.err), "\n", " ")
	}
	return content
}

func (m *model) refreshToolDisplay(previousCollapsedEditBlocks, previousGroupToolVerbs bool) {
	if m.runner == nil || strings.TrimSpace(m.runner.SessionPath) == "" {
		return
	}
	previous, _, _, err := sessionDisplayTranscript(
		m.runner.SessionPath, m.workspace, previousCollapsedEditBlocks, previousGroupToolVerbs,
	)
	if err != nil || strings.TrimSpace(m.transcript.String()) != strings.TrimSpace(previous) {
		return
	}
	text, messages, expands, err := sessionDisplayTranscript(
		m.runner.SessionPath, m.workspace, m.collapsedEditBlocks, m.groupToolVerbs,
	)
	if err == nil {
		m.replaceDisplayTranscript(text, messages, expands)
	}
}

func settingLine(name string, enabled bool) string {
	value := "off"
	if enabled {
		value = "on"
	}
	return fmt.Sprintf("%s: %s", name, value)
}
