package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lookcorner/go-cli/internal/notify"
	"github.com/lookcorner/go-cli/internal/tools"
)

// notifyModel builds a model whose notifications are enabled for every event
// and whose emitted sequences are captured instead of written to stderr.
func notifyModel(t *testing.T, condition string) (*model, *[]string) {
	t.Helper()
	bridge := NewBridge(context.Background(), tools.PermissionAuto)
	t.Cleanup(bridge.Close)
	emitted := &[]string{}
	settings := notify.Settings{Condition: condition, IdleThreshold: 3 * time.Second, Events: notify.Events}
	m := &model{
		bridge: bridge, width: 80, height: 24, notifyTitle: "workspace",
		notifier:   notify.New(settings, notify.Terminal{Brand: "kitty"}),
		notifySink: func(sequence string) { *emitted = append(*emitted, sequence) },
	}
	return m, emitted
}

func TestNotifiesTurnOutcomesWhenUnfocused(t *testing.T) {
	m, emitted := notifyModel(t, notify.ConditionUnfocused)

	m.Update(turnDoneEvent{})
	if len(*emitted) != 0 {
		t.Fatalf("focused terminal notified: %q", *emitted)
	}

	m.Update(tea.BlurMsg{})
	m.Update(turnDoneEvent{})
	if len(*emitted) != 0 {
		t.Fatalf("notified inside the idle threshold: %q", *emitted)
	}

	m.notifier.Blur(time.Now().Add(-time.Minute))
	m.Update(turnDoneEvent{})
	m.Update(turnDoneEvent{err: errors.New("boom")})
	if len(*emitted) != 2 ||
		!strings.Contains((*emitted)[0], "Turn complete · workspace") ||
		!strings.Contains((*emitted)[1], "Agent error · workspace") {
		t.Fatalf("emitted=%q", *emitted)
	}

	m.Update(tea.FocusMsg{})
	m.Update(turnDoneEvent{})
	if len(*emitted) != 2 {
		t.Fatalf("notified after refocus: %q", *emitted)
	}
}

func TestNotifiesApprovalOncePerQueuedBatch(t *testing.T) {
	m, emitted := notifyModel(t, notify.ConditionAlways)

	first := approvalEvent{action: "shell", detail: "ls"}
	updated, _ := m.Update(first)
	m = updated.(*model)
	updated, _ = m.Update(approvalEvent{action: "shell", detail: "pwd"})
	m = updated.(*model)
	if len(*emitted) != 1 || !strings.Contains((*emitted)[0], "Approval required · workspace") {
		t.Fatalf("queued approvals emitted=%q", *emitted)
	}

	m.approval = nil
	updated, _ = m.Update(approvalEvent{action: "shell", detail: "id"})
	if len(*emitted) != 2 {
		t.Fatalf("drained queue did not notify again: %q", *emitted)
	}
}

func TestUnconfiguredEventsAndDisabledPolicyStaySilent(t *testing.T) {
	bridge := NewBridge(context.Background(), tools.PermissionAuto)
	t.Cleanup(bridge.Close)
	emitted := []string{}
	settings := notify.Settings{Condition: notify.ConditionAlways, Events: []notify.Event{notify.SessionReady}}
	m := &model{
		bridge: bridge, width: 80, height: 24,
		notifier:   notify.New(settings, notify.Terminal{Brand: "kitty"}),
		notifySink: func(sequence string) { emitted = append(emitted, sequence) },
	}
	m.Update(turnDoneEvent{})
	m.Update(approvalEvent{action: "shell"})
	if len(emitted) != 0 {
		t.Fatalf("unconfigured events emitted %q", emitted)
	}
	m.notifyEvent(notify.SessionReady)
	if len(emitted) != 1 {
		t.Fatalf("configured event did not emit: %q", emitted)
	}

	bare := &model{bridge: bridge, width: 80, height: 24}
	bare.notifyEvent(notify.TurnComplete)
	bare.Update(turnDoneEvent{})
}

func TestFocusReportingFollowsNotificationPolicy(t *testing.T) {
	events := []notify.Event{notify.TurnComplete}
	for _, test := range []struct {
		name     string
		settings notify.Settings
		want     bool
	}{
		{"unfocused needs focus", notify.Settings{Condition: notify.ConditionUnfocused, Events: events}, true},
		{"always ignores focus", notify.Settings{Condition: notify.ConditionAlways, Events: events}, false},
		{"never ignores focus", notify.Settings{Condition: notify.ConditionNever, Events: events}, false},
	} {
		m := &model{width: 80, height: 24, notifier: notify.New(test.settings, notify.Terminal{Brand: "kitty"})}
		if got := m.notifier.NeedsFocusReports(); got != test.want {
			t.Errorf("%s: needs focus=%v want %v", test.name, got, test.want)
		}
	}
}
