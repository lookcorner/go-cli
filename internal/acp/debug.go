package acp

import "encoding/json"

func (s *Server) handleDebug(incoming message) {
	var params struct {
		SessionID      string `json:"sessionId"`
		SessionIDSnake string `json:"session_id"`
	}
	if json.Unmarshal(incoming.Params, &params) != nil {
		s.respondError(incoming.ID, -32602, "invalid debug parameters")
		return
	}
	if params.SessionID == "" {
		params.SessionID = params.SessionIDSnake
	}
	if params.SessionID == "" {
		s.respondError(incoming.ID, -32602, "sessionId required")
		return
	}
	current := s.lookupSession(params.SessionID)
	if current == nil {
		s.respondError(incoming.ID, -32602, "unknown session id")
		return
	}
	current.mu.Lock()
	runner := current.runner
	closed := current.closed
	current.mu.Unlock()
	if closed || runner == nil {
		s.respondError(incoming.ID, -32602, "unknown session id")
		return
	}
	runner.ArmAutoCompact()
	s.respond(incoming.ID, map[string]any{"result": map[string]any{"armed": true}})
}
