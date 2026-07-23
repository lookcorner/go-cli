package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	guides "github.com/lookcorner/go-cli/internal/docs"
)

type docsState struct {
	guides     []guides.Guide
	selected   int
	guide      *guides.Guide
	standalone bool
}

func (m *model) openDocs() {
	m.docs = &docsState{guides: guides.All()}
	m.scroll = 0
	m.status = "how-to guides"
}

func (m *model) openGuide(guide guides.Guide, standalone bool) {
	m.docs = &docsState{guides: guides.All(), guide: &guide, standalone: standalone}
	m.scroll = 0
	m.status = "guide: " + guide.Title
}

func (m *model) handleDocsKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	state := m.docs
	key, stroke := msg.Key(), msg.Keystroke()
	if stroke == "ctrl+q" {
		return m, tea.Quit
	}
	if key.Code == tea.KeyEsc {
		if state.guide != nil && !state.standalone {
			state.guide = nil
			m.scroll = 0
			m.status = "how-to guides"
		} else {
			m.docs = nil
			m.scroll = 0
			m.status = "ready"
		}
		return m, nil
	}
	if state.guide != nil {
		switch {
		case key.Code == tea.KeyUp || stroke == "ctrl+k" || stroke == "pgup":
			m.scroll = min(m.scroll+max(m.contentHeight()/2, 1), m.maxDocsScroll())
		case key.Code == tea.KeyDown || stroke == "ctrl+j" || stroke == "pgdown":
			m.scroll = max(m.scroll-max(m.contentHeight()/2, 1), 0)
		case key.Code == tea.KeyHome:
			m.scroll = m.maxDocsScroll()
		case key.Code == tea.KeyEnd:
			m.scroll = 0
		}
		return m, nil
	}
	if key.Code == tea.KeyUp || key.Text == "k" {
		state.selected = max(0, state.selected-1)
		return m, nil
	}
	if key.Code == tea.KeyDown || key.Text == "j" {
		state.selected = min(len(state.guides)-1, state.selected+1)
		return m, nil
	}
	if key.Code == tea.KeyEnter && len(state.guides) > 0 {
		guide := state.guides[state.selected]
		state.guide = &guide
		m.scroll = 0
		m.status = "guide: " + guide.Title
	}
	return m, nil
}

func (m *model) docsContent() string {
	state := m.docs
	if state == nil {
		return ""
	}
	if state.guide != nil {
		return state.guide.Content
	}
	lines := make([]string, 0, len(state.guides))
	for _, item := range state.guides {
		lines = append(lines, fmt.Sprintf("%s - %s", escapeDocsText(item.Title), escapeDocsText(item.Description)))
	}
	return "# How-to Guides\n\n" + selectedWindow(lines, state.selected, max(m.contentHeight()-4, 1))
}

func (m *model) docsHint() string {
	if m.docs != nil && m.docs.guide != nil {
		if m.docs.standalone {
			return "Up/Down scroll | Home/End jump | Esc close"
		}
		return "Up/Down scroll | Home/End jump | Esc guides"
	}
	return "Up/Down or j/k select | Enter open | Esc close"
}

func (m *model) maxDocsScroll() int {
	if m.docs == nil || m.docs.guide == nil {
		return 0
	}
	return max(len(renderMarkdownTheme(m.docs.guide.Content, m.transcriptRenderWidth(), m.hyperlinks, m.colors()))-m.contentHeight(), 0)
}

func escapeDocsText(value string) string {
	value = strings.ReplaceAll(sanitizeTerminalText(value), "\n", " ")
	return strings.NewReplacer("\\", "\\\\", "`", "\\`", "*", "\\*", "_", "\\_", "[", "\\[", "]", "\\]").Replace(value)
}
