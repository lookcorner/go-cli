package tui

import (
	"fmt"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/lookcorner/go-cli/internal/session"
)

type sessionSelectState struct {
	dir      string
	sessions []session.Info
	selected int
	loading  bool
	err      string
}

type sessionSelectLoadedEvent struct {
	dir      string
	sessions []session.Info
	err      error
}

func (m *model) openSessionSelect() tea.Cmd {
	if m.runner == nil || strings.TrimSpace(m.runner.SessionPath) == "" {
		m.status = "session resume unavailable"
		return nil
	}
	dir := filepath.Dir(m.runner.SessionPath)
	m.sessionSelect = &sessionSelectState{dir: dir, loading: true}
	m.scroll = 0
	m.status = "loading sessions"
	return func() tea.Msg {
		items, err := session.List(dir, "")
		return sessionSelectLoadedEvent{dir: dir, sessions: items, err: err}
	}
}

func (m *model) finishSessionSelectLoad(event sessionSelectLoadedEvent) {
	state := m.sessionSelect
	if state == nil || state.dir != event.dir {
		return
	}
	state.loading = false
	if event.err != nil {
		state.err = event.err.Error()
		m.status = "list sessions failed"
		return
	}
	state.sessions = event.sessions
	for index, item := range state.sessions {
		if item.SessionID != m.runner.SessionID {
			state.selected = index
			break
		}
	}
	if len(state.sessions) == 0 {
		m.status = "no sessions found"
	} else {
		m.status = "select a session"
	}
}

func (m *model) handleSessionSelectKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	state := m.sessionSelect
	key := msg.Key()
	if key.Code == tea.KeyEsc {
		m.sessionSelect = nil
		m.status = "ready"
		return m, nil
	}
	if state.loading || state.err != "" {
		return m, nil
	}
	if key.Code == tea.KeyUp || key.Text == "k" {
		state.selected = max(0, state.selected-1)
		return m, nil
	}
	if key.Code == tea.KeyDown || key.Text == "j" {
		state.selected = min(max(len(state.sessions)-1, 0), state.selected+1)
		return m, nil
	}
	if key.Code != tea.KeyEnter || len(state.sessions) == 0 {
		return m, nil
	}
	selected := state.sessions[state.selected]
	if selected.SessionID == m.runner.SessionID {
		m.sessionSelect = nil
		m.status = "session already active"
		return m, nil
	}
	path, err := session.PathForID(state.dir, selected.SessionID)
	if err != nil {
		m.status = "resume session: " + err.Error()
		return m, nil
	}
	m.resumeSession = &ResumeSessionError{Path: path, Workspace: selected.CWD}
	m.status = "resuming session"
	return m, tea.Quit
}

func (m *model) sessionSelectContent() string {
	state := m.sessionSelect
	if state == nil {
		return ""
	}
	if state.loading {
		return "# Resume session\n\nLoading sessions..."
	}
	if state.err != "" {
		return "# Resume session\n\nCouldn't list sessions: " + state.err
	}
	if len(state.sessions) == 0 {
		return "# Resume session\n\nNo sessions found."
	}
	lines := make([]string, 0, len(state.sessions))
	for _, item := range state.sessions {
		title := strings.TrimSpace(item.Title)
		if title == "" {
			title = "Untitled session"
		}
		suffix := ""
		if item.SessionID == m.runner.SessionID {
			suffix = " (current)"
		}
		updated := item.UpdatedAt.Local().Format("2006-01-02 15:04")
		lines = append(lines, fmt.Sprintf("%s%s · %s · %s · %s", title, suffix, item.SessionID, item.CWD, updated))
	}
	return "# Resume session\n\n" + selectedWindow(lines, state.selected, max(m.contentHeight()-4, 1))
}
