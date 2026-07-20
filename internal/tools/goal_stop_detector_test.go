package tools

import "testing"

func TestMatchedGoalStopPattern(t *testing.T) {
	tests := map[string]string{
		"I can't proceed.":              "unable_to_proceed",
		"Giving up.":                    "giving_up",
		"Stopping here.":                "stopping_here",
		"3 agents in flight.":           "agents_in_flight",
		"I'll check back in 5 minutes.": "check_back_later",
		"VERDICT: PASS":                 "verdict_line",
		"Committed as `abcdef1`":        "commit_push_pr",
		"Ready for review.":             "ready_for_review",
		"Please run X for me.":          "please_deflection",
	}
	for input, want := range tests {
		if got := matchedGoalStopPattern(input); got != want {
			t.Errorf("matchedGoalStopPattern(%q)=%q, want %q", input, got, want)
		}
	}
	for _, input := range []string{
		"Earlier paragraph: Giving up.\r\n\r\nFinal paragraph says normal work continues.",
		"I'll check back when you confirm.",
		"Stopping hereafter we ship.",
	} {
		if got := matchedGoalStopPattern(input); got != "" {
			t.Errorf("matchedGoalStopPattern(%q)=%q, want no match", input, got)
		}
	}
}

func TestDetectGoalPrematureStopRequiresActiveGoalAndPendingTodo(t *testing.T) {
	registry := &Registry{goal: NewGoalStore(), todos: newTodoStore()}
	recorder := &goalEventRecorder{}
	registry.SetGoalObserver(recorder)
	if err := registry.BeginGoal("finish work"); err != nil {
		t.Fatal(err)
	}
	if got := registry.DetectGoalPrematureStop("Giving up."); got != "" {
		t.Fatalf("matched without pending todo: %q", got)
	}
	registry.todos.items = []todoItem{{id: "work", status: "in_progress"}}
	if got := registry.DetectGoalPrematureStop("Giving up."); got != "giving_up" {
		t.Fatalf("pattern=%q", got)
	}
	if events := recorder.matching("goal_premature_stop_detected"); len(events) != 1 || events[0].Data["pattern"] != "giving_up" {
		t.Fatalf("events=%#v", recorder.events)
	}
	updates := recorder.matching("goal_updated")
	if last := updates[len(updates)-1].Data; last["last_event"] != "premature_stop_detected" || last["premature_stop_pattern"] != "giving_up" {
		t.Fatalf("last update=%#v", last)
	}
}
