package tui

import (
	"fmt"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
)

type jumpEntry struct {
	message int
	preview string
}

type jumpState struct {
	entries       []jumpEntry
	selected      int
	restore       int
	restoreTail   int
	restoreAnchor *int
}

func (m *model) openJump() {
	entries := m.jumpEntries()
	if len(entries) < 2 {
		m.status = "Nothing to jump to yet"
		return
	}
	if m.btwRunning {
		m.status = "wait for the side question before jumping"
		return
	}
	m.selection = nil
	m.selectionClick = selectionClickState{}
	m.scrollSearch = nil
	m.historySearch = nil
	selected := m.activeJumpEntry(entries)
	m.jump = &jumpState{
		entries: entries, selected: selected, restore: m.scroll, restoreTail: m.scrollTail,
		restoreAnchor: cloneInt(m.scrollAnchor),
	}
	m.syncJumpPreview()
	m.status = "choose a turn"
}

func (m *model) jumpEntries() []jumpEntry {
	text := m.transcript.String()
	entries := make([]jumpEntry, 0, len(m.transcriptMessages)/2)
	for index, message := range m.transcriptMessages {
		if message.role != "user" || message.offset < 0 || message.offset > len(text) {
			continue
		}
		end := len(text)
		if index+1 < len(m.transcriptMessages) {
			end = m.transcriptMessages[index+1].start
		}
		if end < message.offset || end > len(text) {
			continue
		}
		entries = append(entries, jumpEntry{message: index, preview: turnPreview(text[message.offset:end])})
	}
	return entries
}

func (m *model) activeJumpEntry(entries []jumpEntry) int {
	lines := renderMarkdownTheme(m.transcriptText(), m.transcriptRenderWidth(), false, m.colors())
	viewportTop := max(len(lines)+m.scrollTail-m.contentHeight()-m.scroll, 0)
	firstBelow := sort.Search(len(entries), func(index int) bool {
		return m.jumpLine(entries[index].message) > viewportTop
	})
	return max(firstBelow-1, 0)
}

func (m *model) handleJumpKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.Key()
	state := m.jump
	switch {
	case key.Code == tea.KeyEsc:
		m.jump = nil
		m.scroll, m.scrollTail, m.scrollAnchor = state.restore, state.restoreTail, cloneInt(state.restoreAnchor)
		m.status = "ready"
	case key.Code == tea.KeyEnter:
		m.jump = nil
		m.status = "ready"
	case key.Code == tea.KeyUp || key.Text == "k":
		state.selected = max(state.selected-1, 0)
		m.syncJumpPreview()
	case key.Code == tea.KeyDown || key.Text == "j":
		state.selected = min(state.selected+1, len(state.entries)-1)
		m.syncJumpPreview()
	}
	return m, nil
}

func (m *model) syncJumpPreview() {
	if m.jump == nil || len(m.jump.entries) == 0 {
		return
	}
	m.anchorTranscriptMessage(m.jump.entries[m.jump.selected].message)
}

func (m *model) jumpLine(messageIndex int) int {
	if messageIndex < 0 || messageIndex >= len(m.transcriptMessages) {
		return 0
	}
	target := m.transcriptMessages[messageIndex].start
	text := m.transcript.String()
	transformed := target
	start := 0
	for index, message := range m.transcriptMessages {
		if message.start < start || message.offset < message.start || message.offset > len(text) {
			continue
		}
		previousUser := index > 0 && m.transcriptMessages[index-1].role == "user"
		if m.effectiveCompact() && (message.role == "user" || previousUser) && message.start >= start+2 && text[message.start-2:message.start] == "\n\n" {
			if message.start <= target {
				transformed--
			}
		}
		if m.showTimestamps && !message.at.IsZero() && message.offset <= target {
			transformed += len("  " + message.at.Local().Format("3:04 PM"))
		}
		start = message.offset
	}
	rendered := m.transcriptText()
	transformed = min(max(transformed, 0), len(rendered))
	return max(len(renderMarkdownTheme(rendered[:transformed], m.transcriptRenderWidth(), false, m.colors()))-1, 0)
}

func turnPreview(value string) string {
	line := ""
	for {
		candidate, rest, found := strings.Cut(value, "\n")
		if candidate = strings.TrimSpace(candidate); candidate != "" {
			line = candidate
			break
		}
		if !found {
			break
		}
		value = rest
	}
	runes := []rune(line)
	if len(runes) <= 120 {
		return line
	}
	return string(runes[:119]) + "…"
}

func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func (m *model) jumpOverlay(lines []string, width int) []string {
	state := m.jump
	if state == nil || len(lines) == 0 {
		return lines
	}
	visibleRows := min(len(state.entries), min(8, max(len(lines)-2, 1)))
	start := max(0, state.selected-visibleRows/2)
	start = min(start, max(len(state.entries)-visibleRows, 0))
	panelWidth := min(max(width-4, 16), 68)
	left := max((width-panelWidth)/2, 0)
	rows := []string{ansiBold + m.colors().modal + fitJumpRow("Jump to which turn?", panelWidth-2) + ansiReset}
	for index := start; index < start+visibleRows; index++ {
		preview := state.entries[index].preview
		if preview == "" {
			preview = "(no preview)"
		}
		row := fmt.Sprintf("%d  %s", index+1, preview)
		row = fitJumpRow(row, panelWidth-2)
		if index == state.selected {
			row = "\x1b[7m" + row + ansiReset
		}
		rows = append(rows, row)
	}
	top := max((len(lines)-len(rows))/2, 0)
	for index, row := range rows {
		lines[top+index] = strings.Repeat(" ", left) + row
	}
	return lines
}

func fitJumpRow(value string, width int) string {
	if displayWidth(value) > width {
		value = fitInputLine([]rune(value), max(width-1, 0)) + "…"
	}
	return value + strings.Repeat(" ", max(width-displayWidth(value), 0))
}
