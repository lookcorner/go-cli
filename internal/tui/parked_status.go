package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/api"
)

type backgroundWork struct {
	commands, monitors, subagents int
}

type parkedWaitState struct {
	callID     string
	prefix     string
	rendered   string
	work       backgroundWork
	suppressed bool
}

func startsParkedWait(call api.ToolCall) bool {
	if call.Name != "get_task_output" {
		return false
	}
	var args struct {
		TimeoutMS uint64 `json:"timeout_ms"`
	}
	return json.Unmarshal(call.Arguments, &args) == nil && args.TimeoutMS > 0
}

func (m *model) startParkedWait(call api.ToolCall) {
	m.parkedWait = &parkedWaitState{callID: call.CallID}
	m.refreshParkedWait(time.Now())
}

func (m *model) finishParkedWait(call api.ToolCall) {
	if m.parkedWait != nil && m.parkedWait.callID == call.CallID {
		m.parkedWait = nil
	}
}

func (m *model) suppressParkedWait() {
	if m.parkedWait != nil {
		m.parkedWait.suppressed = true
	}
}

func (m *model) refreshParkedWait(now time.Time) {
	state := m.parkedWait
	if state == nil || state.suppressed || !m.running || m.runner == nil || m.turnStarted.IsZero() {
		return
	}
	if len(m.pendingPrompts) > 0 {
		state.suppressed = true
		return
	}
	work := runningBackgroundWork(m.runner.TaskSnapshot())
	if work == (backgroundWork{}) || work == state.work {
		return
	}
	marker := fmt.Sprintf("Worked for %s. %s", formatParkedElapsed(now.Sub(m.turnStarted)), work.stillRunningText())
	if state.rendered != "" {
		current := m.transcript.String()
		tailUnchanged := current == state.prefix+state.rendered
		committed := m.minimal && m.minimalCommitted >= len(current)
		if !tailUnchanged {
			state.suppressed = true
			return
		}
		if !committed {
			m.transcript.Reset()
			m.transcript.WriteString(state.prefix)
		}
	}
	state.prefix = m.transcript.String()
	m.appendToolDisplay(marker)
	state.rendered = m.transcript.String()[len(state.prefix):]
	state.work = work
	if m.minimal {
		m.minimalFlushTo = m.transcript.Len()
	}
}

func runningBackgroundWork(snapshot agent.TaskSnapshot) backgroundWork {
	var work backgroundWork
	for _, item := range snapshot.Subagents {
		if item.Background && item.Status == "running" {
			work.subagents++
		}
	}
	for _, item := range snapshot.Processes {
		if item.Completed {
			continue
		}
		if item.Kind == "monitor" {
			work.monitors++
		} else {
			work.commands++
		}
	}
	return work
}

func (w backgroundWork) stillRunningText() string {
	parts := make([]string, 0, 3)
	add := func(count int, noun string) {
		if count == 0 {
			return
		}
		if count != 1 {
			noun += "s"
		}
		parts = append(parts, fmt.Sprintf("%d %s", count, noun))
	}
	add(w.commands, "command")
	add(w.monitors, "monitor")
	add(w.subagents, "subagent")
	if len(parts) == 1 {
		return parts[0] + " still running."
	}
	return strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1] + " still running."
}

func formatParkedElapsed(elapsed time.Duration) string {
	elapsed = max(elapsed, 0)
	if elapsed < time.Minute {
		return fmt.Sprintf("%.1fs", elapsed.Seconds())
	}
	return elapsed.Round(time.Second).String()
}
