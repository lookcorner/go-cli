package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/tools"
)

func eligibleAutomaticRecapModel(now time.Time) *model {
	return &model{
		ctx: context.Background(),
		runner: &agent.Runner{
			Client:    &recapTUIStreamer{},
			SessionID: "session-1",
			Model:     "test",
		},
		previousID:        "response-1",
		status:            "ready",
		sessionRecap:      true,
		automaticRecap:    true,
		recapThreshold:    30 * time.Second,
		recapTurns:        3,
		lastTurnCompleted: now.Add(-4 * time.Minute),
		recapFocusLost:    now.Add(-31 * time.Second),
	}
}

func TestAutomaticRecapPregeneratesAndStaysDisplayOnly(t *testing.T) {
	now := time.Now()
	m := eligibleAutomaticRecapModel(now)
	updated, command := m.update(autoRecapTickEvent{})
	m = updated.(*model)
	if command == nil || !m.recapRunning || !m.recapAttempted.After(now.Add(-time.Second)) {
		t.Fatalf("command=%v running=%v attempted=%v", command != nil, m.recapRunning, m.recapAttempted)
	}
	updated, followup := m.update(command())
	m = updated.(*model)
	if followup != nil || m.recapRunning || !m.recapShownAway || m.lastRecapTurn != 3 ||
		m.status != "ready" || !strings.Contains(m.transcript.String(), "Recap \u2014 We fixed task rendering") {
		t.Fatalf("followup=%v running=%v shown=%v turn=%d status=%q transcript=%q",
			followup != nil, m.recapRunning, m.recapShownAway, m.lastRecapTurn, m.status, m.transcript.String())
	}
}

func TestAutomaticRecapFocusFallbackAndEligibilityGates(t *testing.T) {
	now := time.Now()
	m := eligibleAutomaticRecapModel(now)
	updated, command := m.update(tea.FocusMsg{})
	m = updated.(*model)
	if command == nil || !m.recapRunning || !m.recapFocusLost.IsZero() {
		t.Fatalf("command=%v running=%v lost=%v", command != nil, m.recapRunning, m.recapFocusLost)
	}

	for _, mutate := range []func(*model){
		func(value *model) { value.recapTurns = 2 },
		func(value *model) { value.lastTurnCompleted = now.Add(-time.Minute) },
		func(value *model) { value.settings = &settingsState{} },
	} {
		blocked := eligibleAutomaticRecapModel(now)
		mutate(blocked)
		updated, poll := blocked.update(autoRecapTickEvent{})
		blocked = updated.(*model)
		if poll == nil || blocked.recapRunning {
			t.Fatalf("ineligible recap started: command=%v model=%+v", poll != nil, blocked)
		}
	}
}

func TestAutomaticRecapFocusReportingAndFeatureGate(t *testing.T) {
	enabled := &model{sessionRecap: true, automaticRecap: true}
	if !enabled.View().ReportFocus {
		t.Fatal("automatic recap did not request focus reports")
	}
	disabled := &model{runner: &agent.Runner{SessionID: "session-1"}}
	if disabled.View().ReportFocus {
		t.Fatal("disabled automatic recap requested focus reports")
	}
	disabled.setInput("/recap")
	updated, command := disabled.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	disabled = updated.(*model)
	if command != nil || disabled.status != "session recap is not enabled" {
		t.Fatalf("command=%v status=%q", command != nil, disabled.status)
	}
}

func TestAutomaticRecapHonorsZeroThresholdAndIgnoresFutureSchedules(t *testing.T) {
	now := time.Now()
	m := eligibleAutomaticRecapModel(now)
	m.recapThreshold = 0
	m.recapFocusLost = now
	if !m.recapDue(now) {
		t.Fatal("explicit zero threshold was replaced by the default")
	}
	if autoRecapHasLiveWork(agent.TaskSnapshot{
		Scheduled: []tools.ScheduledTaskCreated{{TaskID: "future"}},
	}) {
		t.Fatal("a future scheduled prompt was treated as running background work")
	}
}
