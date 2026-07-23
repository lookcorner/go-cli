package tui

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lookcorner/go-cli/internal/session"
)

type sessionSelectState struct {
	dir           string
	all           []session.Info
	sessions      []session.Info
	selected      int
	loading       bool
	searching     bool
	deleting      bool
	seq           uint64
	query         []rune
	cursor        int
	searchInput   bool
	pendingDelete string
	err           string
	errTitle      string
}

type sessionSelectLoadedEvent struct {
	dir      string
	sessions []session.Info
	err      error
}

type sessionSelectSearchEvent struct {
	seq    uint64
	result session.SearchResult
	err    error
}

type sessionSelectSearchRequestEvent struct {
	seq   uint64
	query string
}

type sessionSelectDeleteEvent struct {
	dir, id string
	err     error
}

func (m *model) openSessionSelect() tea.Cmd {
	if m.runner == nil || strings.TrimSpace(m.runner.SessionPath) == "" {
		m.status = "session resume unavailable"
		return nil
	}
	dir := filepath.Dir(m.runner.SessionPath)
	m.sessionSelect = &sessionSelectState{dir: dir, loading: true, searchInput: !m.vimMode}
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
		state.errTitle = "Couldn't list sessions"
		m.status = "list sessions failed"
		return
	}
	state.all = event.sessions
	state.sessions = event.sessions
	state.resetSelection(m.runner.SessionID)
	if len(state.sessions) == 0 {
		m.status = "no sessions found"
	} else {
		m.status = "select a session"
	}
}

func (m *model) handleSessionSelectKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	state := m.sessionSelect
	key, stroke := msg.Key(), msg.Keystroke()
	if key.Code == tea.KeyEsc {
		if state.pendingDelete != "" {
			state.pendingDelete = ""
			m.status = "select a session"
			return m, nil
		}
		if m.vimMode && state.searchInput {
			state.searchInput = false
			m.status = "select a session"
			return m, nil
		}
		m.sessionSelect = nil
		m.status = "ready"
		return m, nil
	}
	if state.pendingDelete != "" {
		if strings.EqualFold(key.Text, "n") {
			state.pendingDelete = ""
			m.status = "select a session"
			return m, nil
		}
		if strings.EqualFold(key.Text, "y") || key.Code == tea.KeyEnter {
			id := state.pendingDelete
			state.pendingDelete, state.deleting, state.err = "", true, ""
			m.status = "deleting session"
			return m, runSessionDelete(state.dir, id)
		}
		state.pendingDelete = ""
	}
	if state.loading || state.deleting {
		return m, nil
	}
	if state.err == "" && (stroke == "ctrl+d" || !state.searchInput && strings.EqualFold(key.Text, "d")) && len(state.sessions) > 0 {
		selected := state.sessions[state.selected]
		if selected.SessionID == m.runner.SessionID {
			m.status = "can't delete the active session"
			return m, nil
		}
		state.pendingDelete = selected.SessionID
		m.status = "confirm session deletion"
		return m, nil
	}
	if state.err == "" && (key.Code == tea.KeyUp || !state.searchInput && key.Text == "k") {
		state.selected = max(0, state.selected-1)
		return m, nil
	}
	if state.err == "" && (key.Code == tea.KeyDown || !state.searchInput && key.Text == "j") {
		state.selected = min(max(len(state.sessions)-1, 0), state.selected+1)
		return m, nil
	}
	if !state.searchInput {
		if key.Text == "/" || key.Text == "i" {
			state.searchInput = true
			m.status = "search sessions"
		}
		return m, nil
	}
	changed := false
	switch key.Code {
	case tea.KeyBackspace:
		if state.cursor > 0 {
			state.query = append(state.query[:state.cursor-1], state.query[state.cursor:]...)
			state.cursor--
			changed = true
		}
	case tea.KeyDelete:
		if state.cursor < len(state.query) {
			state.query = append(state.query[:state.cursor], state.query[state.cursor+1:]...)
			changed = true
		}
	case tea.KeyLeft:
		state.cursor = max(0, state.cursor-1)
	case tea.KeyRight:
		state.cursor = min(len(state.query), state.cursor+1)
	case tea.KeyHome:
		state.cursor = 0
	case tea.KeyEnd:
		state.cursor = len(state.query)
	default:
		if key.Text != "" && key.Mod == 0 && stroke != "enter" {
			chars := []rune(key.Text)
			state.query = slices.Insert(state.query, state.cursor, chars...)
			state.cursor += len(chars)
			changed = true
		}
	}
	if changed {
		state.seq++
		state.err, state.errTitle = "", ""
		query := strings.TrimSpace(string(state.query))
		if query == "" {
			state.searching = false
			state.sessions = state.all
			state.resetSelection(m.runner.SessionID)
			m.status = "select a session"
			return m, nil
		}
		state.searching = true
		m.status = "searching sessions"
		return m, queueSessionSearch(query, state.seq)
	}
	if state.searching {
		return m, nil
	}
	if state.err != "" {
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

func (s *sessionSelectState) resetSelection(current string) {
	s.selected = 0
	for index, item := range s.sessions {
		if item.SessionID != current {
			s.selected = index
			return
		}
	}
}

func runSessionSearch(dir, query string, seq uint64) tea.Cmd {
	return func() tea.Msg {
		result, err := session.Search(dir, session.SearchRequest{Query: query, Limit: 100})
		return sessionSelectSearchEvent{seq: seq, result: result, err: err}
	}
}

func queueSessionSearch(query string, seq uint64) tea.Cmd {
	return tea.Tick(150*time.Millisecond, func(time.Time) tea.Msg {
		return sessionSelectSearchRequestEvent{seq: seq, query: query}
	})
}

func (m *model) startSessionSelectSearch(event sessionSelectSearchRequestEvent) tea.Cmd {
	state := m.sessionSelect
	if state == nil || event.seq != state.seq {
		return nil
	}
	return runSessionSearch(state.dir, event.query, event.seq)
}

func (m *model) finishSessionSelectSearch(event sessionSelectSearchEvent) {
	state := m.sessionSelect
	if state == nil || event.seq != state.seq {
		return
	}
	state.searching = false
	if event.err != nil {
		state.err = event.err.Error()
		state.errTitle = "Couldn't search sessions"
		m.status = "search sessions failed"
		return
	}
	byID := make(map[string]session.Info, len(state.all))
	for _, item := range state.all {
		byID[item.SessionID] = item
	}
	state.sessions = make([]session.Info, 0, len(event.result.Results))
	for _, hit := range event.result.Results {
		item, ok := byID[hit.SessionID]
		if !ok {
			updated, _ := time.Parse(time.RFC3339, hit.UpdatedAt)
			item = session.Info{SessionID: hit.SessionID, CWD: hit.CWD, Title: hit.Summary, UpdatedAt: updated}
		}
		state.sessions = append(state.sessions, item)
	}
	state.resetSelection(m.runner.SessionID)
	if len(state.sessions) == 0 {
		m.status = "no matching sessions"
	} else {
		m.status = "select a session"
	}
}

func runSessionDelete(dir, id string) tea.Cmd {
	return func() tea.Msg { return sessionSelectDeleteEvent{dir: dir, id: id, err: session.Delete(dir, id)} }
}

func (m *model) finishSessionSelectDelete(event sessionSelectDeleteEvent) {
	state := m.sessionSelect
	if state == nil || state.dir != event.dir {
		return
	}
	state.deleting = false
	if event.err != nil {
		state.err = event.err.Error()
		state.errTitle = "Couldn't delete session"
		m.status = "delete session failed"
		return
	}
	state.all = removeSession(state.all, event.id)
	state.sessions = removeSession(state.sessions, event.id)
	state.resetSelection(m.runner.SessionID)
	m.status = "session deleted"
}

func removeSession(items []session.Info, id string) []session.Info {
	result := make([]session.Info, 0, len(items))
	for _, item := range items {
		if item.SessionID != id {
			result = append(result, item)
		}
	}
	return result
}

func (m *model) sessionSelectContent() string {
	state := m.sessionSelect
	if state == nil {
		return ""
	}
	if state.loading {
		return "# Resume session\n\nLoading sessions..."
	}
	if state.deleting {
		return "# Resume session\n\nDeleting session..."
	}
	if state.err != "" {
		return "# Resume session\n\n" + state.errTitle + ": " + state.err
	}
	if len(state.sessions) == 0 {
		if strings.TrimSpace(string(state.query)) != "" {
			return "# Resume session\n\nNo matching sessions."
		}
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

func (m *model) sessionSelectHint() string {
	state := m.sessionSelect
	if state.pendingDelete != "" {
		return "Delete " + state.pendingDelete + "? Y confirm · N/Esc cancel"
	}
	if state.loading {
		return "Loading sessions · Esc cancel"
	}
	if state.deleting {
		return "Deleting session · Esc close"
	}
	if m.vimMode && !state.searchInput {
		return "J/K select · Enter resume · D delete · / or I search · Esc cancel"
	}
	return "Type to search · Up/Down select · Enter resume · Ctrl-D delete · Esc cancel"
}
