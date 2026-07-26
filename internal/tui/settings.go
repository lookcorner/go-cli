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

const settingsCount = 34

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
	fixedCount := m.fixedSettingsCount()
	if selected >= fixedCount {
		switch {
		case selected == fixedCount && m.planModeAvailable():
			state.err = errorString(m.setPlanMode(!m.planMode))
		case selected == fixedCount+m.planSettingCount() && m.voiceModeSettingAvailable():
			previous := m.voiceCaptureMode
			if previous == "hold" {
				m.voiceCaptureMode = "toggle"
			} else {
				m.voiceCaptureMode = "hold"
			}
			if m.persistVoiceMode != nil {
				state.err = persistSetting(m.persistVoiceMode(m.voiceCaptureMode), func() { m.voiceCaptureMode = previous })
			}
		case selected == fixedCount+m.planSettingCount()+m.voiceModeSettingCount() && m.voiceClient != nil:
			previous := m.voiceLanguage
			m.voiceLanguage = nextVoiceLanguage(previous)
			if m.persistVoiceLanguage != nil {
				state.err = persistSetting(m.persistVoiceLanguage(m.voiceLanguage), func() { m.voiceLanguage = previous })
			}
		}
		if state.err != "" {
			m.status = "setting update failed"
		} else {
			m.status = "settings updated"
		}
		return
	}
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
		previous := m.defaultPermission
		m.defaultPermission = nextDefaultSelectedPermission(previous)
		if m.persistPermission != nil {
			state.err = persistSetting(m.persistPermission(m.defaultPermission), func() { m.defaultPermission = previous })
		}
	case 22:
		m.openModelSelectFromSettings("default")
		return
	case 23:
		m.openModelSelectFromSettings("fork")
		return
	case 24:
		previous := m.cancelSubagents
		m.cancelSubagents = nextCancelSubagentsPolicy(previous)
		if m.persistCancelSubs != nil {
			state.err = persistSetting(m.persistCancelSubs(m.cancelSubagents), func() { m.cancelSubagents = previous })
		}
	case 25:
		previous := m.undoHint.enabled
		m.undoHint.enabled = !previous
		if m.undoHint.persist != nil {
			state.err = persistSetting(m.undoHint.persist(m.undoHint.enabled), func() { m.undoHint.enabled = previous })
		}
	case 26:
		previous := m.planModeHint.enabled
		m.planModeHint.enabled = !previous
		if m.planModeHint.persist != nil {
			state.err = persistSetting(m.planModeHint.persist(m.planModeHint.enabled), func() { m.planModeHint.enabled = previous })
		}
	case 27:
		previous := m.imageInputHint.enabled
		m.imageInputHint.enabled = !previous
		if m.imageInputHint.persist != nil {
			state.err = persistSetting(m.imageInputHint.persist(m.imageInputHint.enabled), func() { m.imageInputHint.enabled = previous })
		}
		if state.err == "" && !m.imageInputHint.enabled {
			m.imageInputHint.active = false
		}
	case 28:
		previous := m.sendNowHint.enabled
		m.sendNowHint.enabled = !previous
		if m.sendNowHint.persist != nil {
			state.err = persistSetting(m.sendNowHint.persist(m.sendNowHint.enabled), func() { m.sendNowHint.enabled = previous })
		}
	case 29:
		previous := m.smallScreenHint.enabled
		m.smallScreenHint.enabled = !previous
		if m.smallScreenHint.persist != nil {
			state.err = persistSetting(m.smallScreenHint.persist(m.smallScreenHint.enabled), func() { m.smallScreenHint.enabled = previous })
		}
	case 30:
		previous := m.wordSelectHint.enabled
		m.wordSelectHint.enabled = !previous
		if m.wordSelectHint.persist != nil {
			state.err = persistSetting(m.wordSelectHint.persist(m.wordSelectHint.enabled), func() { m.wordSelectHint.enabled = previous })
		}
		if state.err == "" && !m.wordSelectHint.enabled {
			m.wordSelectHint.active = false
		}
	case 31:
		m.settings.number = &settingsNumber{value: normalizedThoughtWidth(m.maxThoughtsWidth), min: 40, max: 500, small: 5, large: 10}
		m.status = "editing setting"
		return
	case 32:
		previous := m.showThinking
		m.showThinking = !previous
		if m.persistThinking != nil {
			state.err = persistSetting(m.persistThinking(m.showThinking), func() { m.showThinking = previous })
		}
		if state.err == "" && !m.minimal {
			m.refreshToolDisplay(m.collapsedEditBlocks, m.groupToolVerbs, previous)
		}
	case 33:
		if m.minimal {
			return
		}
		previous := m.matchRefresh
		m.matchRefresh = !previous
		if m.persistRefresh != nil {
			state.err = persistSetting(m.persistRefresh(m.matchRefresh), func() { m.matchRefresh = previous })
		}
		restartRequired = true
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
	case 31:
		previous := m.maxThoughtsWidth
		if m.persistThoughtWidth != nil {
			state.err = errorString(m.persistThoughtWidth(value))
		}
		if state.err == "" {
			m.maxThoughtsWidth = normalizedThoughtWidth(value)
			m.refreshThoughtWidth(previous)
		} else {
			m.maxThoughtsWidth = previous
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

func nextDefaultSelectedPermission(current string) string {
	switch current {
	case "always_allow_all_sessions":
		return "allow_command_always"
	case "allow_command_always":
		return "allow_once"
	case "allow_once":
		return "reject"
	default:
		return "always_allow_all_sessions"
	}
}

func nextCancelSubagentsPolicy(current string) string {
	switch currentCancelSubagentsPolicy(current) {
	case "ask":
		return "always_stop"
	case "always_stop":
		return "always_continue"
	default:
		return "ask"
	}
}

func currentCancelSubagentsPolicy(current string) string {
	switch current {
	case "always_stop", "always_continue":
		return current
	default:
		return "ask"
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
		fmt.Sprintf("Default selected permission: %s", m.defaultPermission),
		fmt.Sprintf("Default model: %s", m.settingsModelName()),
		fmt.Sprintf("Fork secondary model: %s", m.modelOptionName(m.forkSecondaryModel)),
		fmt.Sprintf("Cancel subagents with turn: %s", currentCancelSubagentsPolicy(m.cancelSubagents)),
		settingLine("Undo hint", m.undoHint.enabled),
		settingLine("Plan-mode hint", m.planModeHint.enabled),
		settingLine("Image-input hint", m.imageInputHint.enabled),
		settingLine("Send-now hint", m.sendNowHint.enabled),
		settingLine("Small-screen hint", m.smallScreenHint.enabled),
		settingLine("Word-select hint", m.wordSelectHint.enabled),
		fmt.Sprintf("Max thoughts width: %d", m.settingNumberValue(31, normalizedThoughtWidth(m.maxThoughtsWidth))),
		settingLine("Show thinking blocks", m.showThinking),
	}
	if !m.minimal {
		lines = append(lines, settingLine("Match display refresh rate (restart)", m.matchRefresh))
	}
	if m.planModeAvailable() {
		lines = append(lines, settingLine("Plan mode", m.planMode))
	}
	if m.voiceClient != nil {
		if m.voiceModeSettingAvailable() {
			lines = append(lines, fmt.Sprintf("Voice capture: %s", canonicalVoiceCaptureMode(m.voiceCaptureMode)))
		}
		lines = append(lines, fmt.Sprintf("Voice language: %s", voice.CanonicalLanguage(m.voiceLanguage)))
	}
	content := "# Settings\n\n" + selectedWindow(lines, m.settings.selected, max(m.contentHeight()-3, 1))
	if m.settings.err != "" {
		content += "\n\n**Error:** " + strings.ReplaceAll(sanitizeTerminalText(m.settings.err), "\n", " ")
	}
	return content
}

func (m *model) settingsModelName() string {
	return m.modelOptionName(m.defaultModelID)
}

func (m *model) modelOptionName(id string) string {
	if id == "" {
		return "(no override)"
	}
	if m.runner != nil {
		for _, option := range m.runner.ModelOptions {
			if option.ID != id {
				continue
			}
			if name := strings.TrimSpace(option.Name); name != "" {
				return name
			}
			if name := strings.TrimSpace(option.Model); name != "" {
				return name
			}
			return option.ID
		}
	}
	return id
}

func (m *model) settingsCount() int {
	count := m.fixedSettingsCount() + m.planSettingCount()
	if m.voiceClient != nil {
		count += 1 + m.voiceModeSettingCount()
	}
	return count
}

func (m *model) fixedSettingsCount() int {
	if m.minimal {
		return settingsCount - 1
	}
	return settingsCount
}

func (m *model) voiceModeSettingCount() int {
	if m.voiceModeSettingAvailable() {
		return 1
	}
	return 0
}

func (m *model) voiceModeSettingAvailable() bool {
	return m.voiceClient != nil && m.voiceKeyReleases
}

func (m *model) planSettingCount() int {
	if m.planModeAvailable() {
		return 1
	}
	return 0
}

func (m *model) planModeAvailable() bool {
	return m.runner != nil && m.runner.Tools != nil && m.runner.Tools.PlanModeAvailable()
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

func (m *model) refreshToolDisplay(previousCollapsedEditBlocks, previousGroupToolVerbs bool, previousThinking ...bool) {
	if m.runner == nil || strings.TrimSpace(m.runner.SessionPath) == "" {
		return
	}
	beforeThinking := m.showThinking
	if len(previousThinking) > 0 {
		beforeThinking = previousThinking[0]
	}
	previous, _, _, _, err := sessionDisplayTranscript(
		m.runner.SessionPath, m.workspace, previousCollapsedEditBlocks, previousGroupToolVerbs, beforeThinking, m.maxThoughtsWidth, m.enrichReplayImage,
	)
	if err != nil || strings.TrimSpace(m.transcript.String()) != strings.TrimSpace(previous) {
		return
	}
	text, messages, expands, folds, err := sessionDisplayTranscript(
		m.runner.SessionPath, m.workspace, m.collapsedEditBlocks, m.groupToolVerbs, m.showThinking, m.maxThoughtsWidth, m.enrichReplayImage,
	)
	if err == nil {
		m.replaceDisplayTranscript(text, messages, expands, folds)
	}
}

func (m *model) refreshThoughtWidth(previousWidth int) {
	if m.runner == nil || strings.TrimSpace(m.runner.SessionPath) == "" {
		return
	}
	previous, _, _, _, err := sessionDisplayTranscript(
		m.runner.SessionPath, m.workspace, m.collapsedEditBlocks, m.groupToolVerbs, m.showThinking, previousWidth, m.enrichReplayImage,
	)
	if err != nil || strings.TrimSpace(m.transcript.String()) != strings.TrimSpace(previous) {
		return
	}
	text, messages, expands, folds, err := sessionDisplayTranscript(
		m.runner.SessionPath, m.workspace, m.collapsedEditBlocks, m.groupToolVerbs, m.showThinking, m.maxThoughtsWidth, m.enrichReplayImage,
	)
	if err == nil {
		m.replaceDisplayTranscript(text, messages, expands, folds)
	}
}

func settingLine(name string, enabled bool) string {
	value := "off"
	if enabled {
		value = "on"
	}
	return fmt.Sprintf("%s: %s", name, value)
}
