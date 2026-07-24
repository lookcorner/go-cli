package tui

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
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
	cwd       string
	process   tools.ProcessSnapshot
	scheduled tools.ScheduledTaskCreated
	session   session.Info
}

type dashboardState struct {
	rows          []dashboardRow
	selected      int
	busy          bool
	loading       bool
	polling       bool
	ticking       bool
	epoch         uint64
	sessionSeq    uint64
	dir           string
	sessions      []session.Info
	pendingDelete string
	renameID      string
	renameKind    dashboardRowKind
	renameInput   []rune
	renameCursor  int
	searching     bool
	searchInput   []rune
	searchCursor  int
	filterKind    string
	filterValue   string
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
	seq      uint64
	sessions []session.Info
	err      error
}

type dashboardTickEvent struct{ epoch uint64 }

type dashboardPollEvent struct {
	epoch    uint64
	seq      uint64
	sessions []session.Info
	err      error
}

func (m *model) openDashboard() tea.Cmd {
	if m.runner == nil || strings.TrimSpace(m.runner.SessionID) == "" {
		m.status = "no active session"
		return nil
	}
	m.dashboardEpoch++
	state := &dashboardState{epoch: m.dashboardEpoch}
	if strings.TrimSpace(m.runner.SessionPath) != "" {
		state.dir = filepath.Dir(m.runner.SessionPath)
		state.loading = true
		state.sessionSeq++
	}
	m.dashboard = state
	m.refreshDashboard()
	m.scroll = 0
	m.status = "agent dashboard"
	if state.dir == "" {
		state.ticking = true
		return dashboardTick(state.epoch)
	}
	seq := state.sessionSeq
	return func() tea.Msg {
		items, err := session.List(state.dir, "")
		return dashboardLoadedEvent{dir: state.dir, seq: seq, sessions: items, err: err}
	}
}

func (m *model) finishDashboardLoad(event dashboardLoadedEvent) tea.Cmd {
	state := m.dashboard
	if state == nil || state.dir != event.dir || state.sessionSeq != event.seq {
		return nil
	}
	state.loading = false
	if event.err != nil {
		state.err = event.err.Error()
		m.status = "dashboard load failed"
	} else {
		m.applyDashboardSessions(event.sessions)
	}
	if !state.ticking {
		state.ticking = true
		return dashboardTick(state.epoch)
	}
	return nil
}

func (m *model) applyDashboardSessions(items []session.Info) {
	state := m.dashboard
	state.sessions = items
	for _, item := range items {
		if item.SessionID == m.runner.SessionID {
			state.currentTitle = item.Title
			break
		}
	}
	m.refreshDashboard()
}

func dashboardTick(epoch uint64) tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return dashboardTickEvent{epoch: epoch} })
}

func (m *model) handleDashboardTick(event dashboardTickEvent) tea.Cmd {
	state := m.dashboard
	if state == nil || state.epoch != event.epoch {
		return nil
	}
	currentErr := state.err
	m.refreshDashboard()
	state.err = currentErr
	next := dashboardTick(state.epoch)
	if state.dir == "" || state.loading || state.polling {
		return next
	}
	state.polling = true
	state.sessionSeq++
	dir, epoch, seq := state.dir, state.epoch, state.sessionSeq
	return tea.Batch(next, func() tea.Msg {
		items, err := session.List(dir, "")
		return dashboardPollEvent{epoch: epoch, seq: seq, sessions: items, err: err}
	})
}

func (m *model) finishDashboardPoll(event dashboardPollEvent) {
	state := m.dashboard
	if state == nil || state.epoch != event.epoch {
		return
	}
	state.polling = false
	if state.sessionSeq != event.seq || event.err != nil {
		return
	}
	currentErr := state.err
	m.applyDashboardSessions(event.sessions)
	state.err = currentErr
}

func (m *model) refreshDashboard() {
	if m.dashboard == nil || m.runner == nil {
		return
	}
	m.dashboardGrouping = dashboardGrouping(m.dashboardGrouping)
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
	rows := []dashboardRow{{kind: dashboardSession, id: m.runner.SessionID, pinned: m.dashboardPins[m.runner.SessionID], status: "active", title: dashboardFirst(state.currentTitle, "Current session"), detail: m.modelName + " · " + m.workspace, cwd: m.workspace}}
	for _, item := range state.sessions {
		if item.SessionID == m.runner.SessionID {
			continue
		}
		title := dashboardFirst(item.Title, item.SessionID)
		cwd := dashboardFirst(item.DisplayCWD, item.CWD)
		detail := dashboardFirst(item.ModelID, "unknown model") + " · " + cwd
		rows = append(rows, dashboardRow{kind: dashboardStoredSession, id: item.SessionID, pinned: m.dashboardPins[item.SessionID], status: "idle", title: title, detail: detail, cwd: cwd, session: item})
	}
	snapshot := m.runner.TaskSnapshot()
	subagents := slices.Clone(snapshot.Subagents)
	sort.Slice(subagents, func(i, j int) bool {
		if (subagents[i].Status == "running") != (subagents[j].Status == "running") {
			return subagents[i].Status == "running"
		}
		return subagents[i].StartedAtMS > subagents[j].StartedAtMS
	})
	for _, item := range subagents {
		rows = append(rows, dashboardRow{kind: dashboardSubagent, id: item.ID, status: dashboardFirst(item.Status, "done"), title: dashboardFirst(item.Description, item.Type), detail: dashboardSubagentMetrics(item), cwd: dashboardFirst(item.WorktreeDir, m.workspace)})
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
		rows = append(rows, dashboardRow{kind: dashboardProcess, id: item.TaskID, status: status, title: dashboardFirst(firstNonemptyLine(item.Description), firstNonemptyLine(item.Command)), detail: dashboardProcessMetrics(item), cwd: dashboardFirst(item.CWD, m.workspace), process: item})
	}
	for _, item := range snapshot.Scheduled {
		rows = append(rows, dashboardRow{kind: dashboardScheduled, id: item.TaskID, status: "scheduled", title: firstNonemptyLine(item.Prompt), detail: item.HumanSchedule, cwd: m.workspace, scheduled: item})
	}
	rows = filterDashboardRows(rows, state.filterKind, state.filterValue)
	m.sortDashboardRows(rows)
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
	lastGroup := ""
	for index, row := range state.rows {
		if m.dashboardGrouping == "state" {
			group := dashboardRowGroup(row)
			if row.pinned {
				group = "pinned"
			}
			if group != lastGroup {
				fmt.Fprintf(&out, "  %s (%d)\n", dashboardGroupLabel(group), dashboardGroupCount(state.rows[index:], group))
				lastGroup = group
			}
		}
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
	if state.searching {
		input := slices.Insert(slices.Clone(state.searchInput), state.searchCursor, '|')
		out.WriteString("\nSearch: " + string(input) + "\n")
	} else if state.filterKind != "" {
		out.WriteString("\nFilter: " + dashboardFilterDisplay(state.filterKind, state.filterValue) + "\n")
	}
	if !state.loading && len(state.rows) == 0 {
		out.WriteString("\nNo dashboard items match the current filter.\n")
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
	if m.dashboard != nil && m.dashboard.searching {
		return "Type to filter | Enter keep | Esc cancel | Ctrl+/ cancel"
	}
	return "Enter view/switch | Ctrl+/ search | Ctrl+G group | Ctrl+T pin | Shift+Up/Down reorder | Ctrl+R rename | X stop/cancel | D delete | R refresh | Esc close"
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
	if state.searching {
		return m.handleDashboardSearchKey(msg)
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
	if stroke == "ctrl+/" {
		m.startDashboardSearch()
		return m, nil
	}
	if stroke == "esc" && state.filterKind != "" {
		state.filterKind, state.filterValue = "", ""
		m.refreshDashboard()
		m.status = "dashboard filter cleared"
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
	case stroke == "ctrl+g":
		m.toggleDashboardGrouping()
	case (stroke == "shift+up" || stroke == "shift+down") && len(state.rows) > 0:
		m.reorderDashboard(state.rows[state.selected], stroke == "shift+up")
	case (stroke == "up" || text == "k") && len(state.rows) > 0:
		state.selected = max(0, state.selected-1)
	case (stroke == "down" || text == "j") && len(state.rows) > 0:
		state.selected = min(len(state.rows)-1, state.selected+1)
	case text == "r":
		m.refreshDashboard()
		m.status = "dashboard refreshed"
		if state.dir != "" {
			state.loading = true
			state.sessionSeq++
			dir, seq := state.dir, state.sessionSeq
			return m, func() tea.Msg {
				items, err := session.List(dir, "")
				return dashboardLoadedEvent{dir: dir, seq: seq, sessions: items, err: err}
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

func (m *model) sortDashboardRows(rows []dashboardRow) {
	order := make(map[string]int, len(m.dashboardOrder))
	for i, id := range m.dashboardOrder {
		order[id] = i
	}
	sort.SliceStable(rows, func(i, j int) bool {
		left, right := rows[i], rows[j]
		if left.pinned != right.pinned {
			return left.pinned
		}
		if m.dashboardGrouping == "directory" {
			if left.cwd != right.cwd {
				return left.cwd < right.cwd
			}
		} else if leftGroup, rightGroup := dashboardRowGroup(left), dashboardRowGroup(right); leftGroup != rightGroup {
			return dashboardGroupRank(leftGroup) < dashboardGroupRank(rightGroup)
		}
		leftPos, leftOrdered := order[left.id]
		rightPos, rightOrdered := order[right.id]
		if leftOrdered != rightOrdered {
			return leftOrdered
		}
		return leftOrdered && leftPos < rightPos
	})
}

func dashboardGrouping(value string) string {
	if strings.EqualFold(strings.TrimSpace(value), "directory") || strings.EqualFold(strings.TrimSpace(value), "dir") {
		return "directory"
	}
	return "state"
}

func dashboardRowGroup(row dashboardRow) string {
	switch row.status {
	case "needs-input", "needs_input", "awaiting":
		return "awaiting"
	case "active", "running":
		return "working"
	case "blocked", "paused":
		return "blocked"
	case "failed", "killed", "cancelled", "canceled", "error", "errored":
		return "failed"
	case "done", "completed":
		return "done"
	default:
		return "idle"
	}
}

func dashboardGroupRank(group string) int {
	switch group {
	case "awaiting":
		return 0
	case "working":
		return 1
	case "blocked":
		return 2
	case "idle":
		return 3
	case "done":
		return 4
	default:
		return 5
	}
}

func dashboardGroupLabel(group string) string {
	switch group {
	case "pinned":
		return "Pinned"
	case "awaiting":
		return "Awaiting"
	case "working":
		return "Working"
	case "blocked":
		return "Blocked"
	case "idle":
		return "Idle"
	case "done":
		return "Done"
	default:
		return "Failed"
	}
}

func dashboardGroupCount(rows []dashboardRow, group string) int {
	count := 0
	for _, row := range rows {
		current := dashboardRowGroup(row)
		if row.pinned {
			current = "pinned"
		}
		if current != group {
			break
		}
		count++
	}
	return count
}

func filterDashboardRows(rows []dashboardRow, kind, value string) []dashboardRow {
	if kind == "" {
		return rows
	}
	value = strings.ToLower(value)
	return slices.DeleteFunc(rows, func(row dashboardRow) bool {
		switch kind {
		case "agent":
			return !strings.Contains(strings.ToLower(row.title), value)
		case "state":
			return dashboardRowGroup(row) != value
		default:
			text := strings.ToLower(row.title + "\n" + row.cwd)
			return !strings.Contains(text, value)
		}
	})
}

func dashboardFilter(query string) (string, string) {
	query = strings.TrimSpace(query)
	if query == "" {
		return "", ""
	}
	if value, ok := strings.CutPrefix(query, "a:"); ok {
		value = strings.TrimSpace(value)
		if value == "" {
			return "", ""
		}
		return "agent", value
	}
	if value, ok := strings.CutPrefix(query, "s:"); ok {
		value = strings.TrimSpace(value)
		if value == "" {
			return "", ""
		}
		if state := dashboardFilterState(value); state != "" {
			return "state", state
		}
		return "text", value
	}
	return "text", query
}

func dashboardFilterState(value string) string {
	value = strings.ToLower(value)
	value = strings.NewReplacer("-", "", "_", "", " ", "").Replace(value)
	switch value {
	case "needsinput", "needs", "input":
		return "awaiting"
	case "working", "busy", "running":
		return "working"
	case "idle", "inactive", "dormant", "scheduled":
		return "idle"
	case "completed", "done":
		return "done"
	case "failed", "errored", "cancelled", "canceled":
		return "failed"
	case "blocked", "paused":
		return "blocked"
	default:
		return ""
	}
}

func dashboardFilterDisplay(kind, value string) string {
	switch kind {
	case "agent":
		return "a:" + value
	case "state":
		return "s:" + value
	default:
		return value
	}
}

func dashboardSubagentMetrics(item tools.SubagentResult) string {
	parts := []string{dashboardFirst(item.Type, "subagent")}
	if item.DurationMS > 0 {
		elapsed := (time.Duration(item.DurationMS) * time.Millisecond).Round(time.Second)
		parts = append(parts, max(elapsed, time.Second).String())
	}
	if item.ToolCalls > 0 {
		parts = append(parts, dashboardMetricCount(item.ToolCalls, "tool"))
	}
	if item.TokensUsed > 0 {
		parts = append(parts, fmt.Sprintf("%d tok", item.TokensUsed))
	}
	if item.Turns > 0 {
		parts = append(parts, dashboardMetricCount(item.Turns, "turn"))
	}
	if item.ContextUsage > 0 {
		parts = append(parts, fmt.Sprintf("%d%% ctx", item.ContextUsage))
	}
	return strings.Join(parts, " · ")
}

func dashboardProcessMetrics(item tools.ProcessSnapshot) string {
	parts := []string{dashboardFirst(item.Kind, "process")}
	if item.Completed {
		switch {
		case item.Signal != nil:
			parts = append(parts, "signal "+*item.Signal)
		case item.ExitCode != nil:
			parts = append(parts, fmt.Sprintf("exit %d", *item.ExitCode))
		}
	}
	started := time.Unix(item.StartTime.SecsSinceEpoch, int64(item.StartTime.NanosSinceEpoch))
	if item.StartTime.SecsSinceEpoch > 0 {
		ended := time.Now()
		if item.EndTime != nil {
			ended = time.Unix(item.EndTime.SecsSinceEpoch, int64(item.EndTime.NanosSinceEpoch))
		}
		if elapsed := ended.Sub(started).Round(time.Second); elapsed >= 0 {
			parts = append(parts, elapsed.String())
		}
	}
	return strings.Join(parts, " · ")
}

func dashboardMetricCount(value int, label string) string {
	if value != 1 {
		label += "s"
	}
	return fmt.Sprintf("%d %s", value, label)
}

func (m *model) startDashboardSearch() {
	state := m.dashboard
	state.searching = true
	state.searchInput = nil
	state.searchCursor = 0
	state.filterKind, state.filterValue = "", ""
	state.err = ""
	m.refreshDashboard()
	m.status = "search dashboard"
}

func (m *model) handleDashboardSearchKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	state := m.dashboard
	key := msg.Key()
	if key.Code == tea.KeyEsc || msg.Keystroke() == "ctrl+/" {
		state.searching = false
		state.searchInput = nil
		state.searchCursor = 0
		state.filterKind, state.filterValue = "", ""
		m.refreshDashboard()
		m.status = "agent dashboard"
		return m, nil
	}
	if key.Code == tea.KeyEnter {
		state.searching = false
		state.searchInput = nil
		state.searchCursor = 0
		m.status = "dashboard filter applied"
		return m, nil
	}
	state.searchInput, state.searchCursor = editDashboardText(state.searchInput, state.searchCursor, key)
	state.filterKind, state.filterValue = dashboardFilter(string(state.searchInput))
	m.refreshDashboard()
	return m, nil
}

func (m *model) toggleDashboardGrouping() {
	state := m.dashboard
	previous := m.dashboardGrouping
	if previous == "state" {
		m.dashboardGrouping = "directory"
	} else {
		m.dashboardGrouping = "state"
	}
	if m.persistGrouping != nil {
		if err := m.persistGrouping(m.dashboardGrouping); err != nil {
			m.dashboardGrouping = previous
			state.err = err.Error()
			m.status = "dashboard grouping failed"
			return
		}
	}
	m.refreshDashboard()
	m.status = "dashboard grouped by " + m.dashboardGrouping
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
	state.renameInput, state.renameCursor = editDashboardText(state.renameInput, state.renameCursor, key)
	state.err = ""
	return m, nil
}

func editDashboardText(input []rune, cursor int, key tea.Key) ([]rune, int) {
	switch key.Code {
	case tea.KeyBackspace:
		if cursor > 0 {
			input = append(input[:cursor-1], input[cursor:]...)
			cursor--
		}
	case tea.KeyDelete:
		if cursor < len(input) {
			input = append(input[:cursor], input[cursor+1:]...)
		}
	case tea.KeyLeft:
		cursor = max(0, cursor-1)
	case tea.KeyRight:
		cursor = min(len(input), cursor+1)
	case tea.KeyHome:
		cursor = 0
	case tea.KeyEnd:
		cursor = len(input)
	default:
		if key.Text != "" && key.Mod == 0 && len(input) < 100 {
			chars := slices.DeleteFunc([]rune(key.Text), unicode.IsControl)
			chars = chars[:min(len(chars), 100-len(input))]
			input = slices.Insert(input, cursor, chars...)
			cursor += len(chars)
		}
	}
	return input, cursor
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
	if row.kind == dashboardScheduled {
		if m.runner.Tools == nil {
			state.err = "Scheduled task cancellation is unavailable"
			return m, nil
		}
		state.busy = true
		return m, func() tea.Msg {
			removed, err := m.runner.Tools.DeleteScheduledTask(row.id)
			if err == nil && !removed {
				err = fmt.Errorf("scheduled task %s was not found", row.id)
			}
			return dashboardDoneEvent{action: "stop", id: row.id, text: "scheduled task cancelled", err: err}
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
