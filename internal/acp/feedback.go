package acp

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lookcorner/go-cli/internal/agent"
	sessionlog "github.com/lookcorner/go-cli/internal/session"
)

const feedbackDisabledMessage = "Feedback is disabled. To enable, set GROK_FEEDBACK_ENABLED=true or [features] feedback = true in config.toml."

type feedbackInput struct {
	SessionID          string          `json:"session_id"`
	SessionIDCamel     string          `json:"sessionId"`
	ClientType         string          `json:"client_type"`
	RatingType         string          `json:"rating_type"`
	RatingValue        *int            `json:"rating_value"`
	FeedbackText       *string         `json:"feedback_text"`
	FeedbackCategories []string        `json:"feedback_categories"`
	ContextType        string          `json:"context_type"`
	TurnNumber         *int64          `json:"turn_number"`
	TurnNumberCamel    *int64          `json:"turnNumber"`
	RequestID          string          `json:"request_id"`
	ClientVersion      string          `json:"client_version"`
	Metadata           json.RawMessage `json:"metadata"`
	TerminalInfo       json.RawMessage `json:"terminal_info"`
}

type feedbackDismissInput struct {
	SessionID      string `json:"session_id"`
	SessionIDCamel string `json:"sessionId"`
	RequestID      string `json:"request_id"`
	RequestIDCamel string `json:"requestId"`
}

func (s *Server) handleFeedback(incoming message) {
	if incoming.Method == "x.ai/feedback/dismiss" {
		s.handleFeedbackDismiss(incoming)
		return
	}
	var input feedbackInput
	if json.Unmarshal(incoming.Params, &input) != nil {
		s.respondErrorData(incoming.ID, -32602, "Invalid params", "invalid feedback parameters")
		return
	}
	if input.SessionID == "" {
		input.SessionID = input.SessionIDCamel
	}
	current := s.lookupSession(input.SessionID)
	if current == nil {
		s.respondErrorData(incoming.ID, -32602, "Invalid params", "session not found: "+input.SessionID)
		return
	}
	current.mu.Lock()
	runner, promptIndex := current.runner, current.promptIndex
	current.mu.Unlock()
	if runner == nil || runner.SubmitFeedback == nil {
		s.respondErrorData(incoming.ID, -32603, "Internal error", feedbackDisabledMessage)
		return
	}
	if input.ClientType == "" && input.FeedbackText == nil {
		s.respondErrorData(incoming.ID, -32602, "Invalid params", "invalid feedback parameters")
		return
	}
	turn := input.TurnNumber
	if turn == nil {
		turn = input.TurnNumberCamel
	}
	if turn == nil {
		value := int64(max(0, promptIndex-1))
		turn = &value
	}
	clientType := input.ClientType
	if clientType == "" {
		clientType = "tui"
	}
	text := ""
	if input.FeedbackText != nil {
		text = *input.FeedbackText
	}
	feedback := sessionlog.UserFeedback{
		TurnNumber: turn, Solicited: input.RequestID != "", RequestID: input.RequestID,
		ClientType: clientType, RatingType: input.RatingType,
		RatingValue: clampFeedbackRating(input.RatingType, input.RatingValue), Text: text,
		Categories: input.FeedbackCategories, ContextType: input.ContextType,
		Metadata: input.Metadata, TerminalInfo: input.TerminalInfo, ClientVersion: input.ClientVersion,
	}
	if err := runner.SubmitFeedback(feedback); err != nil {
		s.respondErrorData(incoming.ID, -32603, "Internal error", "Feedback submission failed: "+err.Error())
		return
	}
	s.respond(incoming.ID, map[string]any{"success": true})
}

func (s *Server) handleFeedbackDismiss(incoming message) {
	var input feedbackDismissInput
	if json.Unmarshal(incoming.Params, &input) != nil {
		s.respondErrorData(incoming.ID, -32602, "Invalid params", "invalid feedback dismiss parameters")
		return
	}
	if input.SessionID == "" {
		input.SessionID = input.SessionIDCamel
	}
	if input.RequestID == "" {
		input.RequestID = input.RequestIDCamel
	}
	if input.SessionID == "" || input.RequestID == "" {
		s.respondErrorData(incoming.ID, -32602, "Invalid params", "session_id and request_id are required")
		return
	}
	current := s.lookupSession(input.SessionID)
	if current == nil {
		s.respondErrorData(incoming.ID, -32602, "Invalid params", "session not found: "+input.SessionID)
		return
	}
	current.mu.Lock()
	runner := current.runner
	current.mu.Unlock()
	if runner == nil || runner.SubmitFeedback == nil {
		s.respondErrorData(incoming.ID, -32603, "Internal error", feedbackDisabledMessage)
		return
	}
	if err := runner.SubmitFeedback(sessionlog.UserFeedback{Solicited: true, RequestID: input.RequestID, Dismissed: true}); err != nil {
		s.respondErrorData(incoming.ID, -32603, "Internal error", "Feedback submission failed: "+err.Error())
		return
	}
	// The reference persists dismissals before reporting the missing remote client.
	s.respondErrorData(incoming.ID, -32603, "Internal error", "No credentials for feedback")
}

func clampFeedbackRating(kind string, value *int) *int {
	if value == nil {
		return nil
	}
	clamped := *value
	switch strings.ToLower(kind) {
	case "thumbs":
		clamped = min(1, max(-1, clamped))
	case "stars":
		clamped = min(5, max(1, clamped))
	case "nps":
		clamped = min(10, max(0, clamped))
	}
	return &clamped
}

func (s *Server) handleFeedbackSlashPrompt(incoming message, current *session, lifecycle promptLifecycle, text string) {
	output := "Usage: /feedback <text>"
	if text != "" {
		if err := current.runner.SubmitFeedback(sessionlog.UserFeedback{Text: text, ClientType: "tui"}); err != nil {
			output = fmt.Sprintf("Feedback could not be saved locally: %v", err)
		} else {
			output = "Feedback saved locally; no feedback server is configured for this session."
		}
	}
	s.sendCommandOutput(current.id, output)
	s.finishPrompt(incoming, current, lifecycle, "end_turn", agent.Result{}, nil, "")
}
