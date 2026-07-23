package acp

import (
	"fmt"

	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/api"
	"github.com/lookcorner/go-cli/internal/tools"
)

func (s *Server) NotifyGoalEvent(sessionID string, event tools.GoalEvent) {
	if s == nil || event.Kind != "goal_updated" {
		return
	}
	update := make(map[string]any, len(event.Data)+1)
	for key, value := range event.Data {
		update[key] = value
	}
	update["sessionUpdate"] = "goal_updated"
	s.write(map[string]any{
		"jsonrpc": "2.0", "method": "x.ai/session_notification",
		"params": map[string]any{"sessionId": sessionID, "update": update},
	})
}

func (s *Server) handleLocalGoalPrompt(incoming message, current *session, lifecycle promptLifecycle, command goalCommand) (string, []api.ContentPart, bool) {
	registry := current.runner.Tools
	if registry == nil || !registry.GoalAvailable() {
		s.failPrompt(incoming, current, lifecycle, "goal mode is unavailable")
		return "", nil, true
	}
	snapshot := registry.GoalSnapshot()
	var text string
	switch command.action {
	case "set":
		if err := registry.BeginGoalWithBudget(command.objective, command.budget); err != nil {
			s.failPrompt(incoming, current, lifecycle, err.Error())
			return "", nil, true
		}
		return command.objective, []api.ContentPart{{Type: "input_text", Text: command.objective}}, false
	case "status":
		text = goalStatusText(snapshot)
	case "pause":
		switch snapshot.Status {
		case "active", "verifying":
			if err := registry.PauseGoalUser(); err != nil {
				s.failPrompt(incoming, current, lifecycle, err.Error())
				return "", nil, true
			}
			text = "Goal paused. Use /goal resume to continue."
		case "user_paused", "back_off_paused", "no_progress_paused", "infra_paused", "blocked", "paused":
			text = "Goal is already paused."
		case "completed":
			text = "Goal is already complete."
		case "budget_limited":
			text = "Goal is budget-limited."
		default:
			text = "No goal is currently set."
		}
	case "resume":
		switch snapshot.Status {
		case "active", "verifying":
			s.sendCommandOutput(current.id, "Goal nudged - refreshing context.")
			return goalResumePrompt(snapshot.Objective), []api.ContentPart{{Type: "input_text", Text: goalResumePrompt(snapshot.Objective)}}, false
		case "user_paused", "back_off_paused", "no_progress_paused", "infra_paused", "blocked", "paused":
			objective, err := registry.ResumeGoal()
			if err != nil {
				s.failPrompt(incoming, current, lifecycle, err.Error())
				return "", nil, true
			}
			s.sendCommandOutput(current.id, "Goal resumed.")
			return goalResumePrompt(objective), []api.ContentPart{{Type: "input_text", Text: goalResumePrompt(objective)}}, false
		case "completed":
			text = "Goal is already complete. Use /goal <objective> to start a new one."
		case "budget_limited":
			text = "Goal is budget-limited. Use /goal clear, then /goal <objective>."
		default:
			text = "No goal set. Use /goal <objective> to start one."
		}
	case "clear":
		if err := registry.ClearGoal(); err != nil {
			s.failPrompt(incoming, current, lifecycle, err.Error())
			return "", nil, true
		}
		text = "Goal cleared."
	default:
		s.failPrompt(incoming, current, lifecycle, fmt.Sprintf("unknown goal action %q", command.action))
		return "", nil, true
	}
	s.sendCommandOutput(current.id, text)
	s.finishPrompt(incoming, current, lifecycle, "end_turn", agent.Result{}, nil, "")
	return "", nil, true
}

func goalResumePrompt(objective string) string {
	return "Continue working toward the active goal:\n" + objective + "\nVerify the remaining work before claiming completion."
}
