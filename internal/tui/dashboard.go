package tui

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/lookcorner/go-cli/internal/tools"
)

type dashboardRowKind uint8

const (
	dashboardSession dashboardRowKind = iota
	dashboardSubagent
	dashboardProcess
	dashboardScheduled
)

type dashboardRow struct {
	kind      dashboardRowKind
	id        string
	status    string
	title     string
	detail    string
	process   tools.ProcessSnapshot
	scheduled tools.ScheduledTaskCreated
}

type dashboardState struct {
	rows     []dashboardRow
	selected int
	busy     bool
	err      string
}

type dashboardDoneEvent struct {
	action string
	id     string
	text   string
	err    error
}

func (m *model) openDashboard() {
	if m.runner == nil || strings.TrimSpace(m.runner.SessionID) == "" {
		m.status = "no active session"
		return
	}
	m.dashboard = &dashboardState{}
	m.refreshDashboard()
	m.scroll = 0
	m.status = "agent dashboard"
}

func (m *model) refreshDashboard() {
	if m.dashboard == nil || m.runner == nil {
		return
	}
	state := m.dashboard
	state.err = ""
	rows := []dashboardRow{{kind: dashboardSession, id: m.runner.SessionID, status: "active", title: "Current session", detail: m.modelName + " · " + m.workspace}}
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
		fmt.Fprintf(&out, "%s%-10s %s\n    %s\n", cursor, row.status, row.title, row.detail)
	}
	if state.err != "" {
		out.WriteString("\nError: " + state.err + "\n")
	}
	return strings.TrimSpace(out.String())
}

func (m *model) dashboardHint() string {
	if m.dashboard != nil && m.dashboard.busy {
		return "Working... | Esc close"
	}
	return "Enter view | X stop running | R refresh | Esc close"
}

func (m *model) handleDashboardKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	state := m.dashboard
	if state == nil {
		return m, nil
	}
	stroke, text := msg.Keystroke(), strings.ToLower(msg.Key().Text)
	if stroke == "esc" || text == "q" {
		m.dashboard = nil
		m.status = "ready"
		return m, nil
	}
	if state.busy {
		return m, nil
	}
	switch {
	case stroke == "up" || text == "k":
		state.selected = max(0, state.selected-1)
	case stroke == "down" || text == "j":
		state.selected = min(len(state.rows)-1, state.selected+1)
	case text == "r":
		m.refreshDashboard()
		m.status = "dashboard refreshed"
	case stroke == "enter" && len(state.rows) > 0:
		return m.openDashboardRow(state.rows[state.selected])
	case text == "x" && len(state.rows) > 0:
		return m.stopDashboardRow(state.rows[state.selected])
	}
	return m, nil
}

func (m *model) openDashboardRow(row dashboardRow) (tea.Model, tea.Cmd) {
	switch row.kind {
	case dashboardSession:
		m.dashboard = nil
		m.viewer = &readOnlyViewer{title: "Current session", content: fmt.Sprintf("# Session info\n\n- Session: `%s`\n- Workspace: `%s`\n- Model: `%s`", row.id, m.workspace, m.modelName)}
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
