package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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

func TestBackgroundTaskCompletionNotifies(t *testing.T) {
	m, emitted := notifyModel(t, notify.ConditionAlways)
	m.bridge.NotifyTaskComplete()
	m.Update(taskCompleteEvent{})
	if len(*emitted) != 1 || !strings.Contains((*emitted)[0], "Task complete · workspace") {
		t.Fatalf("emitted=%q", *emitted)
	}

	var absent *Bridge
	absent.NotifyTaskComplete()
}

func TestNotificationHooksRunForConfiguredEvents(t *testing.T) {
	dir := t.TempDir()
	path := func(name string) string { return filepath.Join(dir, name) }
	m, _ := notifyModel(t, notify.ConditionNever)
	m.notifySessionID = "session-9"
	m.notifyHooks = []notify.Hook{
		{Command: "printf '%s|%s' \"$GROK_EVENT\" \"$GROK_SESSION_ID\" > " + path("all")},
		{Command: "touch " + path("listed"), Events: []notify.Event{notify.TurnComplete}},
		{Command: "touch " + path("unlisted"), Events: []notify.Event{notify.SessionReady}},
		{Command: "touch " + path("away"), OnlyUnfocused: true},
	}

	// The terminal channel is off, so any effect here comes from the hooks.
	m.Update(turnDoneEvent{})

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path("listed")); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	data, err := os.ReadFile(path("all"))
	if err != nil || string(data) != "Turn complete|session-9" {
		t.Fatalf("hook payload=%q err=%v", data, err)
	}
	if _, err = os.Stat(path("unlisted")); err == nil {
		t.Fatal("hook ran for an unconfigured event")
	}
	if _, err = os.Stat(path("away")); err == nil {
		t.Fatal("unfocused-only hook ran while focused")
	}
}

func TestProgressBarTracksTurnLifecycle(t *testing.T) {
	bridge := NewBridge(context.Background(), tools.PermissionAuto)
	t.Cleanup(bridge.Close)
	progressModel := func(enabled bool, brand string) (*model, *[]string) {
		emitted := &[]string{}
		return &model{
			bridge: bridge, width: 80, height: 24,
			progress:   notify.NewProgress(enabled, notify.Terminal{Brand: brand}),
			notifySink: func(sequence string) { *emitted = append(*emitted, sequence) },
		}, emitted
	}

	m, emitted := progressModel(true, "ghostty")
	updated, command := m.Update(progressTickEvent{})
	m = updated.(*model)
	if len(*emitted) != 0 || command != nil || m.progressTicking {
		t.Fatalf("idle session emitted=%q ticking=%v", *emitted, m.progressTicking)
	}

	m.running = true
	updated, command = m.Update(progressTickEvent{})
	m = updated.(*model)
	if len(*emitted) != 1 || (*emitted)[0] != "\x1b]9;4;1;-1\x07" || command == nil || !m.progressTicking {
		t.Fatalf("turn start emitted=%q command=%v ticking=%v", *emitted, command != nil, m.progressTicking)
	}

	updated, _ = m.Update(progressTickEvent{})
	m = updated.(*model)
	if len(*emitted) != 1 {
		t.Fatalf("re-emitted inside keepalive: %q", *emitted)
	}

	updated, _ = m.Update(turnDoneEvent{})
	m = updated.(*model)
	if len(*emitted) != 2 || (*emitted)[1] != "\x1b]9;4;0;0\x07" || m.running {
		t.Fatalf("turn end emitted=%q running=%v", *emitted, m.running)
	}
	m.clearProgress()
	if len(*emitted) != 2 {
		t.Fatalf("exit repeated the clear: %q", *emitted)
	}

	disabled, disabledEmitted := progressModel(false, "ghostty")
	disabled.running = true
	disabled.Update(progressTickEvent{})
	unsupported, unsupportedEmitted := progressModel(true, "kitty")
	unsupported.running = true
	unsupported.Update(progressTickEvent{})
	if len(*disabledEmitted) != 0 || len(*unsupportedEmitted) != 0 {
		t.Fatalf("disabled=%q unsupported=%q", *disabledEmitted, *unsupportedEmitted)
	}
}

func TestTerminalTitleTracksSessionState(t *testing.T) {
	bridge := NewBridge(context.Background(), tools.PermissionAuto)
	t.Cleanup(bridge.Close)
	emitted := []string{}
	items := []string{notify.TitleActionRequired, notify.TitleSpinner, notify.TitleActivity, notify.TitleSessionName, notify.TitleGrok}
	m := &model{
		bridge: bridge, width: 80, height: 24, workspace: "/tmp/demo", notifyTitle: "demo",
		title:      notify.NewTitleManager(true, items),
		notifySink: func(sequence string) { emitted = append(emitted, sequence) },
	}
	titles := func() []string {
		seen := make([]string, 0, len(emitted))
		for _, sequence := range emitted {
			if title, ok := strings.CutPrefix(sequence, "\x1b]0;"); ok {
				seen = append(seen, strings.TrimSuffix(title, "\x07"))
			}
		}
		return seen
	}

	m.Update(titleTickEvent{})
	if got := titles(); len(got) != 1 || got[0] != "demo - gork" {
		t.Fatalf("idle titles=%q", got)
	}

	m.running, m.thoughtOpen = true, true
	updated, command := m.Update(titleTickEvent{})
	m = updated.(*model)
	if got := titles(); len(got) != 2 || got[1] != "⠙ - Thinking - demo - gork" {
		t.Fatalf("busy titles=%q", got)
	}
	if command == nil || !m.titleTicking {
		t.Fatalf("animation tick not armed: command=%v ticking=%v", command != nil, m.titleTicking)
	}

	m.thoughtOpen = false
	m.approval = &approvalEvent{action: "shell"}
	m.Update(titleTickEvent{})
	if got := titles(); len(got) != 3 || !strings.HasPrefix(got[2], "⚠ Action Required - ") {
		t.Fatalf("approval titles=%q", got)
	}

	if escape := m.title.Reset(); escape != "\x1b]0;gork\x07" {
		t.Fatalf("reset=%q", escape)
	}

	disabled := &model{
		bridge: bridge, width: 80, height: 24, running: true,
		title:      notify.NewTitleManager(false, items),
		notifySink: func(string) { t.Error("disabled title manager wrote an escape") },
	}
	if _, command = disabled.Update(titleTickEvent{}); command != nil {
		t.Fatal("disabled title manager armed a tick")
	}
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
