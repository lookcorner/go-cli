package tui

import (
	"context"
	"strconv"

	tea "charm.land/bubbletea/v2"

	"github.com/lookcorner/go-cli/internal/agent"
)

var cancelTurnChoices = []struct {
	label  string
	stop   bool
	always bool
}{
	{label: "Stop running", stop: true},
	{label: "Continue to run"},
	{label: "Always stop", stop: true, always: true},
	{label: "Always continue", always: true},
}

type cancelTurnState struct {
	selected int
	running  []string
}

type cancelSubagentsDoneEvent struct{ errors []string }

func (m *model) requestTurnCancel() tea.Cmd {
	running := m.runningSubagentIDs()
	switch m.cancelSubagents {
	case "always_stop":
		return m.cancelTurnAndSubagents(running)
	case "always_continue":
		return m.cancelTurnAndSubagents(nil)
	default:
		if len(running) > 0 {
			m.cancelTurn = &cancelTurnState{running: running}
			m.status = "choose whether to stop running subagents"
			return nil
		}
		return m.cancelTurnAndSubagents(nil)
	}
}

func (m *model) runningSubagentIDs() []string {
	if m.runner == nil || m.runner.ListSubagents == nil {
		return nil
	}
	var ids []string
	for _, item := range m.runner.ListSubagents() {
		if item.Status == "running" && item.Background {
			ids = append(ids, item.ID)
		}
	}
	return ids
}

func (m *model) handleCancelTurnKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	state := m.cancelTurn
	key, stroke := msg.Key(), msg.Keystroke()
	switch {
	case key.Code == tea.KeyEsc || stroke == "ctrl+c":
		m.cancelTurn = nil
		m.status = "thinking"
		return m, nil
	case key.Code == tea.KeyUp || key.Text == "k":
		state.selected = max(0, state.selected-1)
	case key.Code == tea.KeyDown || key.Text == "j":
		state.selected = min(len(cancelTurnChoices)-1, state.selected+1)
	case key.Code == tea.KeyEnter:
		choice := cancelTurnChoices[state.selected]
		if choice.always {
			previous := m.cancelSubagents
			if choice.stop {
				m.cancelSubagents = "always_stop"
			} else {
				m.cancelSubagents = "always_continue"
			}
			if m.persistCancelSubs != nil {
				if err := m.persistCancelSubs(m.cancelSubagents); err != nil {
					m.cancelSubagents = previous
					m.appendSystem("Could not save cancel-subagents preference: " + err.Error())
				}
			}
		}
		running := state.running
		m.cancelTurn = nil
		if !choice.stop {
			running = nil
		}
		return m, m.cancelTurnAndSubagents(running)
	}
	return m, nil
}

func (m *model) cancelTurnAndSubagents(ids []string) tea.Cmd {
	if m.turnCancel != nil {
		m.turnCancel()
	}
	m.status = "cancelling turn"
	if len(ids) == 0 || m.runner == nil || m.runner.KillSubagent == nil {
		return nil
	}
	return killSubagents(m.ctx, m.runner, ids)
}

func killSubagents(ctx context.Context, runner *agent.Runner, ids []string) tea.Cmd {
	return func() tea.Msg {
		var failures []string
		for _, id := range ids {
			if _, err := runner.KillSubagent(ctx, id); err != nil {
				failures = append(failures, id+": "+err.Error())
			}
		}
		return cancelSubagentsDoneEvent{errors: failures}
	}
}

func (m *model) cancelTurnContent() string {
	if m.cancelTurn == nil {
		return ""
	}
	lines := make([]string, 0, len(cancelTurnChoices))
	for _, choice := range cancelTurnChoices {
		lines = append(lines, choice.label)
	}
	count := len(m.cancelTurn.running)
	return "# Cancel turn\n\n" + pluralCount(count, "subagent") + " running\n\n" + selectedLines(lines, m.cancelTurn.selected)
}

func pluralCount(count int, noun string) string {
	if count != 1 {
		noun += "s"
	}
	return strconv.Itoa(count) + " " + noun
}
