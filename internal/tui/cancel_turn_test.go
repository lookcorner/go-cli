package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/api"
	"github.com/lookcorner/go-cli/internal/tools"
)

func TestPristineTurnCancelRewindsPrompt(t *testing.T) {
	cancelled, requested := false, false
	m := &model{
		ctx: context.Background(), running: true, cancelRewindEnabled: true,
		turnCancel: func() { cancelled = true },
		inFlightPrompt: &inFlightPrompt{
			text: "try again", images: []api.ContentPart{{Type: "input_image", ImageURL: "data:image/png;base64,image"}},
			requestRewind: func() bool { requested = true; return true },
		},
	}
	m.beginTurn("try again")
	updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: 'c', Mod: tea.ModCtrl}))
	m = updated.(*model)
	if command != nil || !cancelled || !requested || m.running ||
		string(m.input) != "try again" || m.cursor != len(m.input) || len(m.promptImages) != 1 ||
		m.transcript.Len() != 0 || len(m.transcriptMessages) != 0 || m.status != "ready" {
		t.Fatalf("cancelled=%v requested=%v running=%v pending=%v input=%q images=%d transcript=%q messages=%#v status=%q",
			cancelled, requested, m.running, false, m.input, len(m.promptImages), m.transcript.String(), m.transcriptMessages, m.status)
	}
	updated, command = m.Update(turnDoneEvent{err: context.Canceled, serial: 1})
	m = updated.(*model)
	if command != nil || string(m.input) != "try again" || m.transcript.Len() != 0 {
		t.Fatalf("completion changed rewind: command=%v input=%q transcript=%q", command != nil, m.input, m.transcript.String())
	}
}

func TestTurnActivityClosesCancelRewindWindow(t *testing.T) {
	for _, event := range []tea.Msg{
		textEvent{text: "hello"},
		thoughtEvent{text: "thinking"},
		toolStartedEvent{call: api.ToolCall{Name: "shell"}},
		toolFinishedEvent{call: api.ToolCall{Name: "shell"}},
	} {
		m := &model{
			ctx: context.Background(), running: true, cancelRewindEnabled: true,
			inFlightPrompt: &inFlightPrompt{text: "request"}, turnCancel: func() {},
		}
		m.beginTurn("request")
		updated, _ := m.Update(event)
		m = updated.(*model)
		if m.inFlightPrompt != nil {
			t.Fatalf("%T left prompt rewindable", event)
		}
	}
}

func TestTurnCancelDoesNotRewindWhenDisabledOrQueued(t *testing.T) {
	for _, test := range []struct {
		name    string
		enabled bool
		queued  []string
	}{
		{name: "disabled"},
		{name: "queued", enabled: true, queued: []string{"next"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			cancelled := false
			m := &model{
				ctx: context.Background(), running: true, cancelRewindEnabled: test.enabled,
				pendingPrompts: test.queued, inFlightPrompt: &inFlightPrompt{text: "request"},
				turnCancel: func() { cancelled = true },
			}
			m.beginTurn("request")
			updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: 'c', Mod: tea.ModCtrl}))
			m = updated.(*model)
			if command != nil || !cancelled || !m.running || string(m.input) != "" ||
				!strings.Contains(m.transcript.String(), "request") || m.status != "cancelling turn" {
				t.Fatalf("command=%v cancelled=%v running=%v pending=%v input=%q transcript=%q status=%q",
					command != nil, cancelled, m.running, false, m.input, m.transcript.String(), m.status)
			}
		})
	}
}

func TestTurnCancelAsksWhenSubagentsAreRunning(t *testing.T) {
	cancelled := false
	m := &model{
		ctx: context.Background(), running: true, status: "thinking", width: 60, height: 16,
		turnCancel: func() { cancelled = true },
		runner: &agent.Runner{ListSubagents: func() []tools.SubagentResult {
			return []tools.SubagentResult{{ID: "running", Status: "running", Background: true}, {ID: "foreground", Status: "running"}, {ID: "done", Status: "completed"}}
		}},
	}
	updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: 'c', Mod: tea.ModCtrl}))
	m = updated.(*model)
	if command != nil || cancelled || m.cancelTurn == nil || len(m.cancelTurn.running) != 1 ||
		!strings.Contains(stripUIANSI(m.View().Content), "1 subagent running") {
		t.Fatalf("command=%v cancelled=%v state=%#v view=%q", command != nil, cancelled, m.cancelTurn, stripUIANSI(m.View().Content))
	}
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEsc}))
	m = updated.(*model)
	if cancelled || m.cancelTurn != nil || m.status != "thinking" {
		t.Fatalf("cancelled=%v state=%#v status=%q", cancelled, m.cancelTurn, m.status)
	}
}

func TestTurnCancelIgnoresForegroundSubagents(t *testing.T) {
	cancelled := false
	m := &model{
		ctx: context.Background(), running: true, turnCancel: func() { cancelled = true },
		runner: &agent.Runner{ListSubagents: func() []tools.SubagentResult {
			return []tools.SubagentResult{{ID: "foreground", Status: "running"}}
		}},
	}
	updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: 'c', Mod: tea.ModCtrl}))
	m = updated.(*model)
	if !cancelled || command != nil || m.cancelTurn != nil || m.status != "cancelling turn" {
		t.Fatalf("cancelled=%v command=%v state=%#v status=%q", cancelled, command != nil, m.cancelTurn, m.status)
	}
}

func TestTurnCancelChoicesStopOrContinueSubagents(t *testing.T) {
	for _, test := range []struct {
		name     string
		selected int
		wantKill bool
	}{
		{name: "stop running", selected: 0, wantKill: true},
		{name: "continue to run", selected: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			cancelled := false
			var killed []string
			m := &model{
				ctx: context.Background(), running: true, turnCancel: func() { cancelled = true },
				cancelTurn: &cancelTurnState{selected: test.selected, running: []string{"one", "two"}},
				runner: &agent.Runner{KillSubagent: func(_ context.Context, id string) (string, error) {
					killed = append(killed, id)
					return "cancelled", nil
				}},
			}
			updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
			m = updated.(*model)
			if !cancelled || m.cancelTurn != nil || (command != nil) != test.wantKill {
				t.Fatalf("cancelled=%v state=%#v command=%v", cancelled, m.cancelTurn, command != nil)
			}
			if command != nil {
				updated, _ = m.Update(command())
				m = updated.(*model)
			}
			if test.wantKill && strings.Join(killed, ",") != "one,two" {
				t.Fatalf("killed=%v", killed)
			}
			if !test.wantKill && len(killed) != 0 {
				t.Fatalf("unexpected kills=%v", killed)
			}
		})
	}
}

func TestTurnCancelAlwaysChoicePersistsAndRollsBack(t *testing.T) {
	var persisted []string
	m := &model{
		ctx: context.Background(), running: true, turnCancel: func() {},
		cancelTurn: &cancelTurnState{selected: 2, running: []string{"one"}},
		runner: &agent.Runner{KillSubagent: func(context.Context, string) (string, error) {
			return "cancelled", nil
		}},
		persistCancelSubs: func(value string) error {
			persisted = append(persisted, value)
			return nil
		},
	}
	updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command == nil || m.cancelSubagents != "always_stop" || strings.Join(persisted, ",") != "always_stop" {
		t.Fatalf("command=%v policy=%q persisted=%v", command != nil, m.cancelSubagents, persisted)
	}

	m.cancelSubagents = "ask"
	m.cancelTurn = &cancelTurnState{selected: 3, running: []string{"two"}}
	m.persistCancelSubs = func(string) error { return errors.New("read only") }
	updated, command = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command != nil || m.cancelSubagents != "ask" || !strings.Contains(m.transcript.String(), "Could not save cancel-subagents preference") {
		t.Fatalf("command=%v policy=%q transcript=%q", command != nil, m.cancelSubagents, m.transcript.String())
	}
}

func TestTurnCancelAlwaysPolicySkipsPromptAndReportsKillFailures(t *testing.T) {
	cancelled := false
	m := &model{
		ctx: context.Background(), running: true, cancelSubagents: "always_stop",
		turnCancel: func() { cancelled = true },
		runner: &agent.Runner{
			ListSubagents: func() []tools.SubagentResult {
				return []tools.SubagentResult{{ID: "one", Status: "running", Background: true}, {ID: "two", Status: "running", Background: true}}
			},
			KillSubagent: func(_ context.Context, id string) (string, error) {
				if id == "two" {
					return "", errors.New("already finished")
				}
				return "cancelled", nil
			},
		},
	}
	updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: 'c', Mod: tea.ModCtrl}))
	m = updated.(*model)
	if !cancelled || command == nil || m.cancelTurn != nil {
		t.Fatalf("cancelled=%v command=%v state=%#v", cancelled, command != nil, m.cancelTurn)
	}
	updated, _ = m.Update(command())
	m = updated.(*model)
	if !strings.Contains(m.transcript.String(), "two: already finished") {
		t.Fatalf("transcript=%q", m.transcript.String())
	}
}

func TestTurnCancelAlwaysContinueSkipsPromptAndKill(t *testing.T) {
	cancelled, killed := false, false
	m := &model{
		ctx: context.Background(), running: true, cancelSubagents: "always_continue",
		turnCancel: func() { cancelled = true },
		runner: &agent.Runner{
			ListSubagents: func() []tools.SubagentResult {
				return []tools.SubagentResult{{ID: "one", Status: "running", Background: true}}
			},
			KillSubagent: func(context.Context, string) (string, error) {
				killed = true
				return "cancelled", nil
			},
		},
	}
	updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: 'c', Mod: tea.ModCtrl}))
	m = updated.(*model)
	if !cancelled || killed || command != nil || m.cancelTurn != nil {
		t.Fatalf("cancelled=%v killed=%v command=%v state=%#v", cancelled, killed, command != nil, m.cancelTurn)
	}
}
