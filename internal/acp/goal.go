package acp

import "github.com/lookcorner/go-cli/internal/tools"

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
