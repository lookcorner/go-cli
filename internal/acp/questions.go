package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/lookcorner/go-cli/internal/tools"
)

func (s *Server) RequestUserQuestion(ctx context.Context, sessionID string, request tools.UserQuestionRequest) (tools.UserQuestionResponse, error) {
	s.beginRosterInteraction(sessionID)
	defer s.endRosterInteraction(sessionID)
	id := fmt.Sprintf("gork-question-%d", s.nextRequest.Add(1))
	result := make(chan userQuestionResult, 1)
	s.mu.Lock()
	s.pendingQuestion[id] = result
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.pendingQuestion, id)
		s.mu.Unlock()
	}()
	s.notifyPendingInteraction(sessionID, request.ToolCallID, "question")
	defer s.notifyInteractionResolved(sessionID, request.ToolCallID)
	type wireQuestion struct {
		Question    string                     `json:"question"`
		Options     []tools.UserQuestionOption `json:"options"`
		MultiSelect bool                       `json:"multiSelect,omitempty"`
	}
	questions := make([]wireQuestion, 0, len(request.Questions))
	for _, question := range request.Questions {
		questions = append(questions, wireQuestion{Question: question.Question, Options: question.Options, MultiSelect: question.MultiSelect})
	}
	s.write(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": "x.ai/ask_user_question",
		"params": map[string]any{
			"sessionId": sessionID, "toolCallId": request.ToolCallID,
			"questions": questions, "mode": request.Mode,
		},
	})
	select {
	case response := <-result:
		return response.response, response.err
	case <-ctx.Done():
		return tools.UserQuestionResponse{}, ctx.Err()
	}
}

func (s *Server) notifyPendingInteraction(sessionID, toolCallID, kind string) {
	s.write(map[string]any{
		"jsonrpc": "2.0", "method": "x.ai/session_notification",
		"params": map[string]any{"sessionId": sessionID, "update": map[string]any{
			"sessionUpdate": "pending_interaction", "tool_call_id": toolCallID, "kind": kind,
		}},
	})
}

func (s *Server) notifyInteractionResolved(sessionID, toolCallID string) {
	s.write(map[string]any{
		"jsonrpc": "2.0", "method": "x.ai/session_notification",
		"params": map[string]any{"sessionId": sessionID, "update": map[string]any{
			"sessionUpdate": "interaction_resolved", "tool_call_id": toolCallID,
		}},
	})
}

func decodeUserQuestionResponse(raw json.RawMessage) (tools.UserQuestionResponse, error) {
	var wire struct {
		Outcome        string                                  `json:"outcome"`
		Answers        map[string]json.RawMessage              `json:"answers"`
		Annotations    map[string]tools.UserQuestionAnnotation `json:"annotations"`
		PartialAnswers map[string]string                       `json:"partial_answers"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return tools.UserQuestionResponse{}, err
	}
	response := tools.UserQuestionResponse{
		Outcome: wire.Outcome, Answers: make(map[string][]string, len(wire.Answers)),
		Annotations: wire.Annotations, PartialAnswers: wire.PartialAnswers,
	}
	for question, rawAnswer := range wire.Answers {
		var answers []string
		if err := json.Unmarshal(rawAnswer, &answers); err != nil {
			var answer string
			if json.Unmarshal(rawAnswer, &answer) != nil {
				return tools.UserQuestionResponse{}, fmt.Errorf("invalid answer for %q", question)
			}
			answers = []string{answer}
		}
		response.Answers[question] = answers
	}
	switch response.Outcome {
	case "accepted", "chat_about_this", "skip_interview", "cancelled":
		return response, nil
	default:
		return tools.UserQuestionResponse{}, errors.New("invalid ACP user question outcome")
	}
}
