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
		"goal_id": "123e4567-e89b-42d3-a456-426614174000", "status": "budget_limited", "phase": "idle", "classifier_runs_attempted": 2,
		"token_budget": int64(100), "tokens_used": int64(105), "last_event": "budget_exceeded",
		"total_worker_rounds": uint32(4), "total_verify_rounds": uint32(2), "token_baseline": int64(0),
		"finished_subagent_tokens": int64(65), "current_subagent_role": "verifier",
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
	if params["sessionId"] != "session-1" || update["sessionUpdate"] != "goal_updated" || update["goal_id"] != "123e4567-e89b-42d3-a456-426614174000" || update["status"] != "budget_limited" || update["classifier_runs_attempted"] != float64(2) || update["token_budget"] != float64(100) || update["tokens_used"] != float64(105) || update["finished_subagent_tokens"] != float64(65) || update["total_worker_rounds"] != float64(4) || update["total_verify_rounds"] != float64(2) || update["current_subagent_role"] != "verifier" || update["last_event"] != "budget_exceeded" {
		t.Fatalf("message=%#v", message)
	}
}
