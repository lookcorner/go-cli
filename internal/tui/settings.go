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

const settingsCount = 5

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
		previousName, previousTheme := m.themeName, m.theme
		m.themeName = nextTheme(m.themeName)
		m.theme = paletteFor(m.themeName)
		if m.persistTheme != nil {
			state.err = persistSetting(m.persistTheme(m.themeName), func() { m.themeName, m.theme = previousName, previousTheme })
		}
	}
	if state.err != "" {
		m.status = "setting update failed"
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
	lines := []string{
		settingLine("Timestamps", m.showTimestamps),
		settingLine("Timeline", m.showTimeline),
		settingLine("Compact mode", m.compactMode),
		settingLine("Vim navigation", m.vimMode),
		fmt.Sprintf("Theme: %s", m.themeName),
	}
	content := "# Settings\n\n" + selectedWindow(lines, m.settings.selected, max(m.contentHeight()-4, 1))
	if m.settings.err != "" {
		content += "\n\n**Error:** " + strings.ReplaceAll(sanitizeTerminalText(m.settings.err), "\n", " ")
	}
	return content
}

func settingLine(name string, enabled bool) string {
	value := "off"
	if enabled {
		value = "on"
	}
	return fmt.Sprintf("%s: %s", name, value)
}
