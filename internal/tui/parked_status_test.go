package tui

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/api"
	"github.com/lookcorner/go-cli/internal/tools"
)

func TestParkedSubagentStatusRefreshesInPlace(t *testing.T) {
	running := 2
	m := &model{
		running: true, turnStarted: time.Now().Add(-24 * time.Second),
		runner: &agent.Runner{ListSubagents: func() []tools.SubagentResult {
			items := make([]tools.SubagentResult, running)
			for index := range items {
				items[index] = tools.SubagentResult{ID: string(rune('a' + index)), Status: "running", Background: true}
			}
			return items
		}},
	}
	call := api.ToolCall{CallID: "wait-1", Name: "get_task_output", Arguments: json.RawMessage(`{"task_ids":["a"],"timeout_ms":30000}`)}
	m.startParkedWait(call)
	if got := m.transcript.String(); !strings.Contains(got, "2 subagents still running.") {
		t.Fatalf("initial marker=%q", got)
	}

	running = 1
	m.refreshParkedWait(time.Now())
	got := m.transcript.String()
	if strings.Count(got, "Worked for") != 1 || !strings.Contains(got, "1 subagent still running.") || strings.Contains(got, "2 subagents") {
		t.Fatalf("marker was duplicated instead of refreshed: %q", got)
	}
}

func TestParkedSubagentStatusDoesNotInterleaveAfterQueuedPrompt(t *testing.T) {
	running := 2
	m := &model{
		running: true, turnStarted: time.Now().Add(-time.Second),
		runner: &agent.Runner{ListSubagents: func() []tools.SubagentResult {
			items := make([]tools.SubagentResult, running)
			for index := range items {
				items[index] = tools.SubagentResult{Status: "running", Background: true}
			}
			return items
		}},
	}
	m.startParkedWait(api.ToolCall{CallID: "wait-1", Name: "get_task_output"})
	before := m.transcript.String()
	m.pendingPrompts = []string{"continue differently"}
	m.suppressParkedWait()
	running = 1
	m.refreshParkedWait(time.Now())
	if got := m.transcript.String(); got != before || strings.Contains(got, "1 subagent still running") {
		t.Fatalf("status interleaved after queued prompt: before=%q after=%q", before, got)
	}
}

func TestCommittedMinimalParkedStatusAppendsRefresh(t *testing.T) {
	running := 2
	m := &model{
		minimal: true, running: true, turnStarted: time.Now(),
		runner: &agent.Runner{ListSubagents: func() []tools.SubagentResult {
			items := make([]tools.SubagentResult, running)
			for index := range items {
				items[index] = tools.SubagentResult{Status: "running", Background: true}
			}
			return items
		}},
	}
	m.startParkedWait(api.ToolCall{CallID: "wait-1"})
	m.minimalCommitted = m.transcript.Len()
	running = 1
	m.refreshParkedWait(time.Now())
	got := m.transcript.String()
	if strings.Count(got, "Worked for") != 2 || !strings.Contains(got, "2 subagents still running.") || !strings.Contains(got, "1 subagent still running.") {
		t.Fatalf("committed marker was rewritten instead of appended: %q", got)
	}
}

func TestParkedWorkStatusFormatsAllBackgroundKinds(t *testing.T) {
	zero := 0
	work := runningBackgroundWork(agent.TaskSnapshot{
		Subagents: []tools.SubagentResult{{Status: "running", Background: true}, {Status: "running"}},
		Processes: []tools.ProcessSnapshot{
			{Kind: "command"}, {Kind: "monitor"}, {Kind: "command", Completed: true, ExitCode: &zero},
		},
	})
	if got, want := work.stillRunningText(), "1 command, 1 monitor and 1 subagent still running."; got != want {
		t.Fatalf("status=%q want=%q", got, want)
	}
}

func TestStartsParkedWaitRequiresPositiveTimeout(t *testing.T) {
	for _, test := range []struct {
		arguments string
		want      bool
	}{
		{arguments: `{"task_ids":["a"],"timeout_ms":1}`, want: true},
		{arguments: `{"task_ids":["a"],"timeout_ms":0}`},
		{arguments: `{}`},
	} {
		if got := startsParkedWait(api.ToolCall{Name: "get_task_output", Arguments: json.RawMessage(test.arguments)}); got != test.want {
			t.Fatalf("arguments=%s got=%v want=%v", test.arguments, got, test.want)
		}
	}
}

func TestToolStartedEventCreatesParkedMarker(t *testing.T) {
	m := &model{
		ctx: context.Background(), running: true, turnStarted: time.Now(),
		runner: &agent.Runner{ListSubagents: func() []tools.SubagentResult {
			return []tools.SubagentResult{{Status: "running", Background: true}}
		}},
	}
	call := api.ToolCall{CallID: "wait-1", Name: "get_task_output", Arguments: json.RawMessage(`{"task_ids":["a"],"timeout_ms":30000}`)}
	updated, command := m.Update(toolStartedEvent{call: call})
	m = updated.(*model)
	if command == nil || m.parkedWait == nil || !strings.Contains(m.transcript.String(), "1 subagent still running.") {
		t.Fatalf("command=%v parked=%#v transcript=%q", command != nil, m.parkedWait, m.transcript.String())
	}
	m.Update(toolFinishedEvent{call: call})
	if m.parkedWait != nil {
		t.Fatalf("parked wait survived tool completion: %#v", m.parkedWait)
	}
}

func TestTurnCompletionClearsParkedWait(t *testing.T) {
	m := &model{running: true, parkedWait: &parkedWaitState{callID: "wait-1"}}
	updated, _ := m.Update(turnDoneEvent{})
	if updated.(*model).parkedWait != nil {
		t.Fatalf("parked wait survived turn completion: %#v", updated.(*model).parkedWait)
	}
}
