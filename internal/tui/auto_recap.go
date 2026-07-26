package tui

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lookcorner/go-cli/internal/agent"
)

const (
	autoRecapRetryInterval = 90 * time.Second
	autoRecapPollInterval  = 20 * time.Second
	minAutoRecapTurns      = 3
	minAutoRecapIdle       = 3 * time.Minute
)

func (m *model) autoRecapEnabled() bool {
	return m.sessionRecap && m.automaticRecap
}

func (m *model) blurForAutomaticRecap(now time.Time) tea.Cmd {
	if !m.autoRecapEnabled() {
		return nil
	}
	m.recapFocusLost = now
	m.recapShownAway = false
	m.recapAttempted = time.Time{}
	return m.ensureAutoRecapTick()
}

func (m *model) focusForAutomaticRecap(now time.Time) tea.Cmd {
	if !m.autoRecapEnabled() {
		m.recapFocusLost = time.Time{}
		return nil
	}
	// Capture due before clearing the away timer (matches Rust FocusGained).
	due := m.recapDue(now)
	m.recapFocusLost = time.Time{}
	if !due {
		return nil
	}
	return m.dispatchAutoRecap(now)
}

func (m *model) pollAutomaticRecap(now time.Time) tea.Cmd {
	m.autoRecapTicking = false
	if !m.autoRecapEnabled() || m.recapFocusLost.IsZero() {
		return nil
	}
	// Prefer a single recap command so callers can execute it directly; the
	// done handler re-arms the away poll when needed.
	if cmd := m.maybePregenerateAwayRecap(now); cmd != nil {
		return cmd
	}
	return m.ensureAutoRecapTick()
}

func (m *model) maybePregenerateAwayRecap(now time.Time) tea.Cmd {
	if !m.recapDue(now) {
		return nil
	}
	return m.dispatchAutoRecap(now)
}

func (m *model) ensureAutoRecapTick() tea.Cmd {
	if m.autoRecapTicking || !m.autoRecapEnabled() || m.recapFocusLost.IsZero() || m.recapShownAway {
		return nil
	}
	m.autoRecapTicking = true
	return tea.Tick(autoRecapPollInterval, func(time.Time) tea.Msg { return autoRecapTickEvent{} })
}

func (m *model) recapDue(now time.Time) bool {
	if !m.autoRecapEnabled() || m.recapShownAway || m.recapFocusLost.IsZero() {
		return false
	}
	if !m.recapAttempted.IsZero() && now.Sub(m.recapAttempted) < autoRecapRetryInterval {
		return false
	}
	return now.Sub(m.recapFocusLost) >= m.recapThreshold
}

// autoRecapUIEligible mirrors the Rust focus-gained / pregenerate UI gates:
// idle session, no modal/question, established session id, no live background work.
func (m *model) autoRecapUIEligible() bool {
	if m.running || m.recapRunning || m.btwRunning {
		return false
	}
	if m.approval != nil || m.question != nil || m.planReview != nil || m.cancelTurn != nil {
		return false
	}
	if m.settings != nil || m.docs != nil || m.sessionSelect != nil || m.modelSelect != nil {
		return false
	}
	if m.runner == nil || strings.TrimSpace(m.runner.SessionID) == "" {
		return false
	}
	if autoRecapHasLiveWork(m.runner.TaskSnapshot()) {
		return false
	}
	return true
}

func (m *model) dispatchAutoRecap(now time.Time) tea.Cmd {
	if !m.autoRecapUIEligible() {
		return nil
	}
	// Retry backoff only — do not consume the away period on dispatch.
	// Shell gates (≥3 turns / ≥3 min idle) may still no-op; we retry later.
	m.recapAttempted = now
	return m.startRecapWithAuto(true)
}

func autoRecapHasLiveWork(snapshot agent.TaskSnapshot) bool {
	for _, subagent := range snapshot.Subagents {
		if subagent.Status == "running" {
			return true
		}
	}
	for _, process := range snapshot.Processes {
		if !process.Completed {
			return true
		}
	}
	return false
}

func (m *model) autoRecapTurnEligible(now time.Time) bool {
	if m.recapTurns < minAutoRecapTurns || m.recapTurns <= m.lastRecapTurn {
		return false
	}
	if m.lastTurnCompleted.IsZero() || now.Sub(m.lastTurnCompleted) < minAutoRecapIdle {
		return false
	}
	return true
}
