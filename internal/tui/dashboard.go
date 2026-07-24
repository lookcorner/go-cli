package tui

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"

	"github.com/lookcorner/go-cli/internal/session"
	"github.com/lookcorner/go-cli/internal/tools"
)

type dashboardRowKind uint8

const (
	dashboardSession dashboardRowKind = iota
	dashboardStoredSession
	dashboardSubagent
	dashboardProcess
	dashboardScheduled
)

type dashboardRow struct {
	kind      dashboardRowKind
	id        string
	pinned    bool
	status    string
	title     string
	detail    string
	process   tools.ProcessSnapshot
	scheduled tools.ScheduledTaskCreated
	session   session.Info
}

type dashboardState struct {
	rows          []dashboardRow
	selected      int
	busy          bool
	loading       bool
	dir           string
	sessions      []session.Info
	pendingDelete string
	renameID      string
	renameKind    dashboardRowKind
	renameInput   []rune
	renameCursor  int
	currentTitle  string
	err           string
}

type dashboardDoneEvent struct {
	action string
	id     string
	text   string
	err    error
}

type dashboardLoadedEvent struct {
	dir      string
	sessions []session.Info
	err      error
}

func (m *model) openDashboard() tea.Cmd {
	if m.runner == nil || strings.TrimSpace(m.runner.SessionID) == "" {
		m.status = "no active session"
		return nil
	}
	state := &dashboardState{}
	if strings.TrimSpace(m.runner.SessionPath) != "" {
		state.dir = filepath.Dir(m.runner.SessionPath)
		state.loading = true
	}
	m.dashboard = state
	m.refreshDashboard()
	m.scroll = 0
	m.status = "agent dashboard"
	if state.dir == "" {
		return nil
	}
	return func() tea.Msg {
		items, err := session.List(state.dir, "")
		return dashboardLoadedEvent{dir: state.dir, sessions: items, err: err}
	}
}

func (m *model) finishDashboardLoad(event dashboardLoadedEvent) {
	state := m.dashboard
	if state == nil || state.dir != event.dir {
		return
	}
	state.loading = false
	if event.err != nil {
		state.err = event.err.Error()
		m.status = "dashboard load failed"
		return
	}
	state.sessions = event.sessions
	for _, item := range event.sessions {
		if item.SessionID == m.runner.SessionID {
			state.currentTitle = item.Title
			break
		}
	}
	m.refreshDashboard()
}

func (m *model) refreshDashboard() {
	if m.dashboard == nil || m.runner == nil {
		return
	}
	state := m.dashboard
	if !state.loading {
		m.cleanDashboardRefs()
	}
	var selected dashboardRow
	hasSelection := state.selected >= 0 && state.selected < len(state.rows)
	if hasSelection {
		selected = state.rows[state.selected]
	}
	state.err = ""
	rows := []dashboardRow{{kind: dashboardSession, id: m.runner.SessionID, pinned: m.dashboardPins[m.runner.SessionID], status: "active", title: dashboardFirst(state.currentTitle, "Current session"), detail: m.modelName + " · " + m.workspace}}
	for _, item := range state.sessions {
		if item.SessionID == m.runner.SessionID {
			continue
		}
		title := dashboardFirst(item.Title, item.SessionID)
		detail := dashboardFirst(item.ModelID, "unknown model") + " · " + dashboardFirst(item.DisplayCWD, item.CWD)
		rows = append(rows, dashboardRow{kind: dashboardStoredSession, id: item.SessionID, pinned: m.dashboardPins[item.SessionID], status: "idle", title: title, detail: detail, session: item})
	}
	order := make(map[string]int, len(m.dashboardOrder))
	for i, id := range m.dashboardOrder {
		order[id] = i
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].pinned != rows[j].pinned {
			return rows[i].pinned
		}
		if rows[i].status != rows[j].status {
			return rows[i].status == "active"
		}
		iPos, iOrdered := order[rows[i].id]
		jPos, jOrdered := order[rows[j].id]
		if iOrdered != jOrdered {
			return iOrdered
		}
		return iOrdered && iPos < jPos
	})
	snapshot := m.runner.TaskSnapshot()
	subagents := slices.Clone(snapshot.Subagents)
	sort.Slice(subagents, func(i, j int) bool {
		if (subagents[i].Status == "running") != (subagents[j].Status == "running") {
			return subagents[i].Status == "running"
		}
		return subagents[i].StartedAtMS > subagents[j].StartedAtMS
	})
	for _, item := range subagents {
		rows = append(rows, dashboardRow{kind: dashboardSubagent, id: item.ID, status: dashboardFirst(item.Status, "done"), title: dashboardFirst(item.Description, item.Type), detail: item.Type})
	}
	processes := slices.Clone(snapshot.Processes)
	sort.Slice(processes, func(i, j int) bool {
		if processes[i].Completed != processes[j].Completed {
			return !processes[i].Completed
		}
		if processes[i].StartTime.SecsSinceEpoch != processes[j].StartTime.SecsSinceEpoch {
			return processes[i].StartTime.SecsSinceEpoch > processes[j].StartTime.SecsSinceEpoch
		}
		return processes[i].StartTime.NanosSinceEpoch > processes[j].StartTime.NanosSinceEpoch
	})
	for _, item := range processes {
		status := "running"
		if item.Completed {
			status = "done"
			if item.ExplicitlyKilled {
				status = "killed"
			} else if item.Signal != nil || item.ExitCode != nil && *item.ExitCode != 0 {
				status = "failed"
			}
		}
		rows = append(rows, dashboardRow{kind: dashboardProcess, id: item.TaskID, status: status, title: dashboardFirst(firstNonemptyLine(item.Description), firstNonemptyLine(item.Command)), detail: dashboardFirst(item.Kind, "process"), process: item})
	}
	for _, item := range snapshot.Scheduled {
		rows = append(rows, dashboardRow{kind: dashboardScheduled, id: item.TaskID, status: "scheduled", title: firstNonemptyLine(item.Prompt), detail: item.HumanSchedule, scheduled: item})
	}
	state.rows = rows
	state.selected = min(state.selected, max(len(rows)-1, 0))
	if hasSelection {
		for i, row := range rows {
			if row.kind == selected.kind && row.id == selected.id {
				state.selected = i
				break
			}
		}
	}
}

func (m *model) dashboardContent() string {
	state := m.dashboard
	if state == nil {
		return ""
	}
	var out strings.Builder
	out.WriteString("# Agent Dashboard\n\n")
	for index, row := range state.rows {
		cursor := "  "
		if index == state.selected {
			cursor = "> "
		}
		pin := "  "
		if row.pinned {
			pin = "* "
		}
		fmt.Fprintf(&out, "%s%s%-10s %s\n      %s\n", cursor, pin, row.status, row.title, row.detail)
	}
	if state.err != "" {
		out.WriteString("\nError: " + state.err + "\n")
	}
	if state.loading {
		out.WriteString("\nLoading sessions...\n")
	}
	if state.pendingDelete != "" {
		out.WriteString("\nDelete session " + state.pendingDelete + "? Press Y to confirm or N to cancel.\n")
	}
	if state.renameID != "" {
		input := slices.Insert(slices.Clone(state.renameInput), state.renameCursor, '|')
		out.WriteString("\nRename: " + string(input) + "\n")
	}
	return strings.TrimSpace(out.String())
}

func (m *model) dashboardHint() string {
	if m.dashboard != nil && m.dashboard.busy {
		return "Working... | Esc close"
	}
	if m.dashboard != nil && m.dashboard.renameID != "" {
		return "Enter save | Esc cancel | Left/Right move cursor"
	}
	return "Enter view/switch | Ctrl+T pin | Shift+Up/Down reorder | Ctrl+R rename | X stop | D delete | R refresh | Esc close"
}

func (m *model) handleDashboardKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	state := m.dashboard
	if state == nil {
		return m, nil
	}
	stroke, text := msg.Keystroke(), strings.ToLower(msg.Key().Text)
	if state.renameID != "" {
		return m.handleDashboardRenameKey(msg)
	}
	if state.pendingDelete != "" {
		if text == "y" || stroke == "enter" {
			id, dir := state.pendingDelete, state.dir
			state.pendingDelete, state.busy = "", true
			return m, func() tea.Msg { return dashboardDoneEvent{action: "delete", id: id, err: session.Delete(dir, id)} }
		}
		if text == "n" || stroke == "esc" {
			state.pendingDelete = ""
			return m, nil
		}
		return m, nil
	}
	if stroke == "esc" || text == "q" {
		m.dashboard = nil
		m.status = "ready"
		return m, nil
	}
	if state.busy {
		return m, nil
	}
	switch {
	case (stroke == "shift+up" || stroke == "shift+down") && len(state.rows) > 0:
		m.reorderDashboard(state.rows[state.selected], stroke == "shift+up")
	case stroke == "up" || text == "k":
		state.selected = max(0, state.selected-1)
	case stroke == "down" || text == "j":
		state.selected = min(len(state.rows)-1, state.selected+1)
	case text == "r":
		m.refreshDashboard()
		m.status = "dashboard refreshed"
		if state.dir != "" {
			state.loading = true
			return m, func() tea.Msg {
				items, err := session.List(state.dir, "")
				return dashboardLoadedEvent{dir: state.dir, sessions: items, err: err}
			}
		}
	case stroke == "enter" && len(state.rows) > 0:
		return m.openDashboardRow(state.rows[state.selected])
	case text == "x" && len(state.rows) > 0:
		return m.stopDashboardRow(state.rows[state.selected])
	case text == "d" && len(state.rows) > 0:
		row := state.rows[state.selected]
		if row.kind != dashboardStoredSession {
			state.err = "Only idle sessions can be deleted"
		} else {
			state.pendingDelete = row.id
		}
	case stroke == "ctrl+t" && len(state.rows) > 0:
		m.toggleDashboardPin(state.rows[state.selected])
	case (text == "e" || stroke == "ctrl+r") && len(state.rows) > 0:
		row := state.rows[state.selected]
		if row.kind != dashboardSession && row.kind != dashboardStoredSession {
			state.err = "Only sessions can be renamed"
		} else {
			state.renameID, state.renameKind = row.id, row.kind
			state.renameInput = nil
			state.renameCursor = 0
			state.err = ""
			m.status = "rename session"
		}
	}
	return m, nil
}

func (m *model) cleanDashboardRefs() {
	alive := map[string]bool{m.runner.SessionID: true}
	for _, item := range m.dashboard.sessions {
		alive[item.SessionID] = true
	}
	for id := range m.dashboardPins {
		if !alive[id] {
			delete(m.dashboardPins, id)
		}
	}
	seen := make(map[string]bool, len(m.dashboardOrder))
	cleaned := m.dashboardOrder[:0]
	for _, id := range m.dashboardOrder {
		if alive[id] && !seen[id] {
			cleaned = append(cleaned, id)
			seen[id] = true
		}
	}
	m.dashboardOrder = cleaned
}

func (m *model) reorderDashboard(row dashboardRow, up bool) {
	state := m.dashboard
	if row.kind != dashboardSession && row.kind != dashboardStoredSession {
		state.err = "Only sessions can be reordered"
		return
	}
	previous := slices.Clone(m.dashboardOrder)
	position := slices.Index(m.dashboardOrder, row.id)
	if up {
		switch {
		case position == 0:
			m.dashboardOrder = m.dashboardOrder[1:]
		case position > 0:
			m.dashboardOrder[position], m.dashboardOrder[position-1] = m.dashboardOrder[position-1], m.dashboardOrder[position]
		default:
			m.dashboardOrder = append([]string{row.id}, m.dashboardOrder...)
		}
	} else {
		switch {
		case position >= 0 && position+1 < len(m.dashboardOrder):
			m.dashboardOrder[position], m.dashboardOrder[position+1] = m.dashboardOrder[position+1], m.dashboardOrder[position]
		case position < 0:
			m.dashboardOrder = append(m.dashboardOrder, row.id)
		}
	}
	if m.persistOrder != nil {
		if err := m.persistOrder(slices.Clone(m.dashboardOrder)); err != nil {
			m.dashboardOrder = previous
			state.err = err.Error()
			m.status = "reorder session failed"
			return
		}
	}
	m.refreshDashboard()
	if up {
		m.status = "session moved up"
	} else {
		m.status = "session moved down"
	}
}

func (m *model) toggleDashboardPin(row dashboardRow) {
	state := m.dashboard
	if row.kind != dashboardSession && row.kind != dashboardStoredSession {
		state.err = "Only sessions can be pinned"
		return
	}
	if m.dashboardPins == nil {
		m.dashboardPins = make(map[string]bool)
	}
	wasPinned := m.dashboardPins[row.id]
	if wasPinned {
		delete(m.dashboardPins, row.id)
	} else {
		m.dashboardPins[row.id] = true
	}
	if m.persistPins != nil {
		if err := m.persistPins(m.dashboardPinnedIDs()); err != nil {
			if wasPinned {
				m.dashboardPins[row.id] = true
			} else {
				delete(m.dashboardPins, row.id)
			}
			state.err = err.Error()
			m.status = "pin session failed"
			return
		}
	}
	m.refreshDashboard()
	if wasPinned {
		m.status = "session unpinned"
	} else {
		m.status = "session pinned"
	}
}

func (m *model) dashboardPinnedIDs() []string {
	ids := make([]string, 0, len(m.dashboardPins))
	for id := range m.dashboardPins {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func (m *model) handleDashboardRenameKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	state := m.dashboard
	key := msg.Key()
	if key.Code == tea.KeyEsc {
		state.renameID, state.renameInput, state.renameCursor = "", nil, 0
		m.status = "agent dashboard"
		return m, nil
	}
	if key.Code == tea.KeyEnter {
		title := strings.TrimSpace(string(state.renameInput))
		if title == "" {
			state.renameID, state.renameInput, state.renameCursor = "", nil, 0
			state.err = ""
			m.status = "agent dashboard"
			return m, nil
		}
		id, kind, dir := state.renameID, state.renameKind, state.dir
		state.renameID, state.renameInput, state.renameCursor, state.busy = "", nil, 0, true
		return m, func() tea.Msg {
			var err error
			if kind == dashboardSession {
				err = m.runner.RenameSession(title)
			} else {
				err = session.Rename(dir, id, title)
			}
			return dashboardDoneEvent{action: "rename", id: id, text: title, err: err}
		}
	}
	switch key.Code {
	case tea.KeyBackspace:
		if state.renameCursor > 0 {
			state.renameInput = append(state.renameInput[:state.renameCursor-1], state.renameInput[state.renameCursor:]...)
			state.renameCursor--
		}
	case tea.KeyDelete:
		if state.renameCursor < len(state.renameInput) {
			state.renameInput = append(state.renameInput[:state.renameCursor], state.renameInput[state.renameCursor+1:]...)
		}
	case tea.KeyLeft:
		state.renameCursor = max(0, state.renameCursor-1)
	case tea.KeyRight:
		state.renameCursor = min(len(state.renameInput), state.renameCursor+1)
	case tea.KeyHome:
		state.renameCursor = 0
	case tea.KeyEnd:
		state.renameCursor = len(state.renameInput)
	default:
		if key.Text != "" && key.Mod == 0 && len(state.renameInput) < 100 {
			chars := []rune(key.Text)
			chars = slices.DeleteFunc(chars, unicode.IsControl)
			chars = chars[:min(len(chars), 100-len(state.renameInput))]
			state.renameInput = slices.Insert(state.renameInput, state.renameCursor, chars...)
			state.renameCursor += len(chars)
		}
	}
	state.err = ""
	return m, nil
}

func (m *model) openDashboardRow(row dashboardRow) (tea.Model, tea.Cmd) {
	switch row.kind {
	case dashboardSession:
		m.dashboard = nil
		m.viewer = &readOnlyViewer{title: "Current session", content: fmt.Sprintf("# Session info\n\n- Session: `%s`\n- Workspace: `%s`\n- Model: `%s`", row.id, m.workspace, m.modelName)}
	case dashboardStoredSession:
		path, err := session.PathForID(m.dashboard.dir, row.id)
		if err != nil {
			m.dashboard.err = err.Error()
			return m, nil
		}
		m.resumeSession = &ResumeSessionError{Path: path, Workspace: row.session.CWD}
		m.status = "resuming session"
		return m, tea.Quit
	case dashboardSubagent:
		if m.runner.GetSubagent == nil {
			m.dashboard.err = "Subagent details are unavailable"
			return m, nil
		}
		m.dashboard.busy = true
		ctx := m.ctx
		if ctx == nil {
			ctx = context.Background()
		}
		return m, func() tea.Msg {
			result, err := m.runner.GetSubagent(ctx, row.id, 0)
			return dashboardDoneEvent{action: "view", id: row.id, text: formatSubagentDetail(result), err: err}
		}
	case dashboardProcess:
		m.dashboard = nil
		m.viewer = &readOnlyViewer{title: "Process: " + row.id, content: fmt.Sprintf("# %s\n\nCommand: `%s`\n\n%s", row.title, row.process.Command, dashboardFirst(row.process.Output, "No output."))}
	case dashboardScheduled:
		m.dashboard = nil
		m.viewer = &readOnlyViewer{title: "Scheduled task: " + row.id, content: fmt.Sprintf("# Scheduled task\n\n- Schedule: %s\n- Prompt: %s", row.scheduled.HumanSchedule, row.scheduled.Prompt)}
	}
	m.scroll = 0
	return m, nil
}

func (m *model) stopDashboardRow(row dashboardRow) (tea.Model, tea.Cmd) {
	state := m.dashboard
	if row.kind == dashboardSubagent && row.status == "running" && m.runner.KillSubagent != nil {
		state.busy = true
		ctx := m.ctx
		if ctx == nil {
			ctx = context.Background()
		}
		return m, func() tea.Msg {
			text, err := m.runner.KillSubagent(ctx, row.id)
			return dashboardDoneEvent{action: "stop", id: row.id, text: text, err: err}
		}
	}
	if row.kind == dashboardProcess && row.status == "running" && m.runner.KillTask != nil {
		state.busy = true
		ctx := m.ctx
		if ctx == nil {
			ctx = context.Background()
		}
		return m, func() tea.Msg {
			text, err := m.runner.KillTask(ctx, row.id)
			return dashboardDoneEvent{action: "stop", id: row.id, text: text, err: err}
		}
	}
	state.err = "Selected item is not running"
	return m, nil
}

func formatSubagentDetail(result tools.SubagentResult) string {
	return fmt.Sprintf("# %s\n\n- ID: `%s`\n- Status: %s\n- Turns: %d\n- Tool calls: %d\n- Tokens: %d\n\n%s", dashboardFirst(result.Description, result.Type), result.ID, result.Status, result.Turns, result.ToolCalls, result.TokensUsed, dashboardFirst(result.Output, "No output yet."))
}

func dashboardFirst(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
