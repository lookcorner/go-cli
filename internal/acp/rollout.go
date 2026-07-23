package acp

import "encoding/json"

func (s *Server) handleRolloutSurvey(incoming message) {
	var params struct {
		SessionID   *string   `json:"sessionId"`
		Preferences *[]string `json:"preferences"`
		Feedback    *string   `json:"feedback"`
	}
	if json.Unmarshal(incoming.Params, &params) != nil || params.SessionID == nil || params.Preferences == nil || params.Feedback == nil {
		s.respondErrorData(incoming.ID, -32602, "Invalid params", "sessionId, preferences, and feedback are required")
		return
	}
	s.respond(incoming.ID, map[string]any{"success": true})
}
