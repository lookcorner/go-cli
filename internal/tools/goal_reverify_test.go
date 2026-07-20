package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func refutedGoalRegistry(t *testing.T) *Registry {
	t.Helper()
	registry := &Registry{goal: NewGoalStore()}
	if err := registry.BeginGoal("finish the feature"); err != nil {
		t.Fatal(err)
	}
	if _, err := (&updateGoalTool{store: registry.goal}).Execute(context.Background(), json.RawMessage(`{"completed":true}`)); err != nil {
		t.Fatal(err)
	}
	if err := registry.StartGoalVerification(10); err != nil {
		t.Fatal(err)
	}
	if err := registry.ResolveGoalVerification(GoalVerification{Summary: "tests still fail"}, 10); err != nil {
		t.Fatal(err)
	}
	return registry
}

func TestGoalReverifyReminderEscalatesAndResets(t *testing.T) {
	registry := refutedGoalRegistry(t)
	if snapshot := registry.GoalSnapshot(); snapshot.RoundsSinceVerify != 0 {
		t.Fatalf("rounds after verification=%d", snapshot.RoundsSinceVerify)
	}
	if reminder, err := registry.GoalReverifyReminder(2); err != nil || reminder != "" {
		t.Fatalf("first reminder=%q err=%v", reminder, err)
	}
	soft, err := registry.GoalReverifyReminder(2)
	if err != nil || !strings.Contains(soft, "Re-verify before continuing.") || !strings.Contains(soft, "2 rounds") || strings.Contains(soft, "STOP DRIFTING") {
		t.Fatalf("soft reminder=%q err=%v", soft, err)
	}
	registry.goal.roundsSinceVerify = 5
	hard, err := registry.GoalReverifyReminder(2)
	if err != nil || !strings.Contains(hard, "STOP DRIFTING \u2014 RE-VERIFY NOW.") || !strings.Contains(hard, "6 rounds") {
		t.Fatalf("hard reminder=%q err=%v", hard, err)
	}
	registry.goal.status = "verifying"
	if err := registry.StartGoalVerification(10); err != nil {
		t.Fatal(err)
	}
	if snapshot := registry.GoalSnapshot(); snapshot.RoundsSinceVerify != 0 {
		t.Fatalf("rounds after reverification=%d", snapshot.RoundsSinceVerify)
	}
}

func TestGoalReverifyReminderRequiresActiveRefutedGoalAndFloorsThreshold(t *testing.T) {
	registry := &Registry{goal: NewGoalStore()}
	if err := registry.BeginGoal("goal"); err != nil {
		t.Fatal(err)
	}
	if reminder, err := registry.GoalReverifyReminder(0); err != nil || reminder != "" || registry.GoalSnapshot().RoundsSinceVerify != 1 {
		t.Fatalf("unrefuted reminder=%q err=%v snapshot=%#v", reminder, err, registry.GoalSnapshot())
	}
	registry.goal.lastVerification = "gap"
	if reminder, err := registry.GoalReverifyReminder(0); err != nil || !strings.Contains(reminder, "2 rounds") {
		t.Fatalf("floored reminder=%q err=%v", reminder, err)
	}
	registry.goal.status = "blocked"
	if reminder, err := registry.GoalReverifyReminder(1); err != nil || reminder != "" || registry.GoalSnapshot().RoundsSinceVerify != 2 {
		t.Fatalf("inactive reminder=%q err=%v snapshot=%#v", reminder, err, registry.GoalSnapshot())
	}
}
