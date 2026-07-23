package acp

import (
	"encoding/json"
	"fmt"
	"time"
)

func (s *Server) handleDebug(incoming message) {
	if incoming.Method == "x.ai/debug/trigger_feedback" {
		s.handleDebugFeedback(incoming)
		return
	}
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

func (s *Server) handleDebugFeedback(incoming message) {
	var params struct {
		SessionID      string `json:"sessionId"`
		SessionIDSnake string `json:"session_id"`
		Tier           string `json:"tier"`
		Mode           string `json:"mode"`
	}
	if json.Unmarshal(incoming.Params, &params) != nil {
		s.respondErrorData(incoming.ID, -32602, "Invalid params", "invalid debug feedback parameters")
		return
	}
	if params.SessionID == "" {
		params.SessionID = params.SessionIDSnake
	}
	if params.SessionID == "" {
		s.respondErrorData(incoming.ID, -32602, "Invalid params", "sessionId required")
		return
	}
	if params.Tier == "" {
		params.Tier = "tier1"
	}
	if params.Mode == "" {
		params.Mode = "thumbs_text"
	}
	prompt, triggerType, triggerReason, ok := debugFeedbackTier(params.Tier)
	if !ok {
		s.respondErrorData(incoming.ID, -32602, "Invalid params", fmt.Sprintf("unknown tier: %q (expected tier1/tier2/tier3)", params.Tier))
		return
	}
	stars, thumbs, text, ok := debugFeedbackMode(params.Mode)
	if !ok {
		s.respondErrorData(incoming.ID, -32602, "Invalid params", fmt.Sprintf("unknown mode: %q (expected thumbs/stars/text/thumbs_text/stars_text)", params.Mode))
		return
	}
	current := s.lookupSession(params.SessionID)
	if current == nil {
		s.respondErrorData(incoming.ID, -32602, "Invalid params", "session not found: "+params.SessionID)
		return
	}
	current.mu.Lock()
	closed, runner := current.closed, current.runner
	current.mu.Unlock()
	if closed || runner == nil {
		s.respondErrorData(incoming.ID, -32602, "Invalid params", "session not found: "+params.SessionID)
		return
	}
	requestID, err := newUUIDv7(time.Now())
	if err != nil {
		s.respondErrorData(incoming.ID, -32603, "Internal error", err.Error())
		return
	}
	notification := map[string]any{
		"request_id": requestID, "tier": params.Tier, "prompt": prompt,
		"dismissible": true, "trigger_type": triggerType,
		"trigger_condition": "debug/trigger_feedback (manual test trigger)",
		"trigger_reason":    triggerReason, "stars": stars, "thumbs": thumbs, "text": text,
	}
	update := make(map[string]any, len(notification)+1)
	for key, value := range notification {
		update[key] = value
	}
	update["sessionUpdate"] = "feedback_request"
	s.notifyXAI(current, update)
	s.respond(incoming.ID, map[string]any{"result": notification})
}

func debugFeedbackTier(tier string) (prompt, triggerType, triggerReason string, ok bool) {
	switch tier {
	case "tier1":
		return "You've been using Grok Code productively! Would you mind sharing quick feedback?", "tier1_engagement", "Tier 1: Sustained engagement (turns=0, tools=0, compactions=0, no cancellations)", true
	case "tier2":
		return "You've worked through a complex session. Your feedback would help us improve.", "tier2_complex_recovery", "Tier 2: Complex session with errors (turns=0, tools=0, compactions=0, errors=0)", true
	case "tier3":
		return "Thanks for sticking with us through that session. Got a moment to share feedback?", "tier3_friction_recovery", "Tier 3: Recovery from friction (turns=0, cancellations=0, reverted=false)", true
	default:
		return "", "", "", false
	}
}

func debugFeedbackMode(mode string) (stars, thumbs, text, ok bool) {
	switch mode {
	case "stars":
		return true, false, false, true
	case "thumbs":
		return false, true, false, true
	case "text":
		return false, false, true, true
	case "stars_text":
		return true, false, true, true
	case "thumbs_text":
		return false, true, true, true
	default:
		return false, false, false, false
	}
}
