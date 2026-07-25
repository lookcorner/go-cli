package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	uitheme "github.com/lookcorner/go-cli/internal/theme"
	"github.com/lookcorner/go-cli/internal/voice"
)

type settingsState struct {
	selected int
	err      string
	number   *settingsNumber
}

type settingsNumber struct {
	value int
	min   int
	max   int
	small int
	large int
}

const settingsCount = 21

func (m *model) openSettings() {
	m.settings = &settingsState{}
	m.scroll = 0
	m.status = "settings"
}

func (m *model) handleSettingsKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.Key()
	if m.settings.number != nil {
		return m.handleSettingsNumber(key)
	}
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
		m.settings.selected = min(m.settingsCount()-1, m.settings.selected+1)
		return m, nil
	}
	if key.Code != tea.KeyEnter && key.Code != tea.KeySpace && key.Text != " " {
		return m, nil
	}
	m.applySetting(m.settings.selected)
	return m, nil
}

func (m *model) handleSettingsNumber(key tea.Key) (tea.Model, tea.Cmd) {
	number := m.settings.number
	switch {
	case key.Code == tea.KeyEsc:
		m.settings.number = nil
		m.status = "settings"
	case key.Code == tea.KeyUp || key.Text == "k":
		number.value = min(number.value+number.small, number.max)
	case key.Code == tea.KeyDown || key.Text == "j":
		number.value = max(number.value-number.small, number.min)
	case key.Code == tea.KeyRight || key.Text == "l":
		number.value = min(number.value+number.large, number.max)
	case key.Code == tea.KeyLeft || key.Text == "h":
		number.value = max(number.value-number.large, number.min)
	case key.Code == tea.KeyEnter:
		m.commitSettingsNumber(number.value)
	}
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
		m.settings.number = &settingsNumber{value: m.effectiveScrollSpeed(), min: 1, max: 100, small: 1, large: 5}
		m.status = "editing setting"
		return
	case 13:
		previous := m.scrollInput
		m.scrollInput = scrollInput{mode: nextScrollMode(previous.mode), serial: previous.serial + 1}
		if m.persistScrollMode != nil {
			state.err = persistSetting(m.persistScrollMode(m.scrollInput.mode), func() { m.scrollInput = previous })
		}
	case 14:
		m.settings.number = &settingsNumber{value: m.effectiveScrollLines(), min: 1, max: 10, small: 1, large: 1}
		m.status = "editing setting"
		return
	case 15:
		previous := m.selectionMode
		m.selectionMode = previous.next()
		if m.persistSelection != nil {
			state.err = persistSetting(m.persistSelection(m.selectionMode.canonical()), func() { m.selectionMode = previous })
		}
	case 16:
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
	case 17:
		previous := m.hunkTrackerMode
		m.hunkTrackerMode = nextHunkTrackerMode(previous)
		if m.persistHunkTracker != nil {
			state.err = persistSetting(m.persistHunkTracker(m.hunkTrackerMode), func() { m.hunkTrackerMode = previous })
		}
		restartRequired = state.err == ""
	case 18:
		previousName, previousTheme := m.autoDarkTheme, m.theme
		m.autoDarkTheme = nextConcreteTheme(concreteAutomaticTheme(previousName, "groknight"))
		m.theme = paletteForAuto(m.themeName, m.autoDarkTheme, m.autoLightTheme)
		if m.persistAutoDark != nil {
			state.err = persistSetting(m.persistAutoDark(m.autoDarkTheme), func() { m.autoDarkTheme, m.theme = previousName, previousTheme })
		}
	case 19:
		previousName, previousTheme := m.autoLightTheme, m.theme
		m.autoLightTheme = nextConcreteTheme(concreteAutomaticTheme(previousName, "grokday"))
		m.theme = paletteForAuto(m.themeName, m.autoDarkTheme, m.autoLightTheme)
		if m.persistAutoLight != nil {
			state.err = persistSetting(m.persistAutoLight(m.autoLightTheme), func() { m.autoLightTheme, m.theme = previousName, previousTheme })
		}
	case 20:
		previousName, previousTheme := m.themeName, m.theme
		m.themeName = nextTheme(m.themeName)
		m.theme = paletteForAuto(m.themeName, m.autoDarkTheme, m.autoLightTheme)
		if m.persistTheme != nil {
			state.err = persistSetting(m.persistTheme(m.themeName), func() { m.themeName, m.theme = previousName, previousTheme })
		}
	case 21:
		previous := m.voiceLanguage
		m.voiceLanguage = nextVoiceLanguage(previous)
		if m.persistVoiceLanguage != nil {
			state.err = persistSetting(m.persistVoiceLanguage(m.voiceLanguage), func() { m.voiceLanguage = previous })
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

func (m *model) commitSettingsNumber(value int) {
	state := m.settings
	state.err = ""
	switch state.selected {
	case 12:
		if m.persistScrollSpeed != nil {
			state.err = errorString(m.persistScrollSpeed(uint8(value)))
		}
		if state.err == "" {
			m.scrollSpeed = uint8(value)
			m.scrollCarry = 0
			m.resetScrollInput()
		}
	case 14:
		if m.persistScrollLines != nil {
			state.err = errorString(m.persistScrollLines(uint8(value)))
		}
		if state.err == "" {
			m.scrollLines = value
			m.resetScrollInput()
		}
	}
	state.number = nil
	if state.err != "" {
		m.status = "setting update failed"
	} else {
		m.status = "settings updated"
	}
}

func (m *model) resetScrollInput() {
	m.scrollInput = scrollInput{mode: m.scrollInput.mode, serial: m.scrollInput.serial + 1}
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
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

func nextConcreteTheme(current string) string {
	for index, name := range uitheme.Names {
		if name == current {
			return uitheme.Names[(index+1)%len(uitheme.Names)]
		}
	}
	return uitheme.Names[0]
}

func nextHunkTrackerMode(current string) string {
	switch currentHunkTrackerMode(current) {
	case "agent_only":
		return "all_dirty"
	case "all_dirty":
		return "off"
	default:
		return "agent_only"
	}
}

func currentHunkTrackerMode(current string) string {
	switch current {
	case "all_dirty", "off":
		return current
	default:
		return "agent_only"
	}
}

func nextVoiceLanguage(current string) string {
	current = voice.CanonicalLanguage(current)
	for index, language := range voice.Languages {
		if language == current {
			return voice.Languages[(index+1)%len(voice.Languages)]
		}
	}
	return voice.Languages[0]
}

func nextScrollMode(current string) string {
	switch current {
	case "auto":
		return "wheel"
	case "wheel":
		return "trackpad"
	default:
		return "auto"
	}
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
		fmt.Sprintf("Scroll speed: %d", m.settingNumberValue(12, m.effectiveScrollSpeed())),
		fmt.Sprintf("Scroll input: %s", scrollModeName(m.scrollInput.mode)),
		fmt.Sprintf("Scroll lines: %d", m.settingNumberValue(14, m.effectiveScrollLines())),
		fmt.Sprintf("Text selection: %s", m.selectionMode.canonical()),
		fmt.Sprintf("Mermaid rendering: %s", mermaidMode),
		fmt.Sprintf("Hunk tracker (restart): %s", currentHunkTrackerMode(m.hunkTrackerMode)),
		fmt.Sprintf("Auto dark theme: %s", concreteAutomaticTheme(m.autoDarkTheme, "groknight")),
		fmt.Sprintf("Auto light theme: %s", concreteAutomaticTheme(m.autoLightTheme, "grokday")),
		fmt.Sprintf("Theme: %s", m.themeName),
	}
	if m.voiceClient != nil {
		lines = append(lines, fmt.Sprintf("Voice language: %s", voice.CanonicalLanguage(m.voiceLanguage)))
	}
	content := "# Settings\n\n" + selectedWindow(lines, m.settings.selected, max(m.contentHeight()-3, 1))
	if m.settings.err != "" {
		content += "\n\n**Error:** " + strings.ReplaceAll(sanitizeTerminalText(m.settings.err), "\n", " ")
	}
	return content
}

func (m *model) settingsCount() int {
	if m.voiceClient != nil {
		return settingsCount + 1
	}
	return settingsCount
}

func (m *model) effectiveScrollSpeed() int {
	if m.scrollSpeed == 0 {
		return 50
	}
	return int(m.scrollSpeed)
}

func (m *model) effectiveScrollLines() int {
	if m.scrollLines == 0 {
		return mouseWheelScrollLines
	}
	return m.scrollLines
}

func (m *model) settingNumberValue(selected, current int) int {
	if m.settings.selected == selected && m.settings.number != nil {
		return m.settings.number.value
	}
	return current
}

func scrollModeName(mode string) string {
	if mode == "wheel" || mode == "trackpad" {
		return mode
	}
	return "auto"
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
