package acp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lookcorner/go-cli/internal/tools"
)

func TestNotifyGoalEventSendsRealtimeGoalUpdateOnly(t *testing.T) {
	var output strings.Builder
	server := &Server{output: &output}
	server.NotifyGoalEvent("session-1", tools.GoalEvent{Kind: "goal_planner_fired", Data: map[string]any{"attempt": 1}})
	if output.Len() != 0 {
		t.Fatalf("trace-only event was sent: %s", output.String())
	}
	server.NotifyGoalEvent("session-1", tools.GoalEvent{Kind: "goal_updated", Data: map[string]any{
		"status": "complete", "phase": "idle", "classifier_runs_attempted": 2,
	}})
	var message map[string]any
	if err := json.Unmarshal([]byte(output.String()), &message); err != nil {
		t.Fatal(err)
	}
	if message["method"] != "x.ai/session_notification" {
		t.Fatalf("message=%#v", message)
	}
	params := message["params"].(map[string]any)
	update := params["update"].(map[string]any)
	if params["sessionId"] != "session-1" || update["sessionUpdate"] != "goal_updated" || update["status"] != "complete" || update["classifier_runs_attempted"] != float64(2) {
		t.Fatalf("message=%#v", message)
	}
}
