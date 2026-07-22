package acp

import (
	"context"
	"encoding/json"
	"time"
)

const suggestPromptTimeout = 45 * time.Second

func (s *Server) handleSuggestPrompt(parent context.Context, incoming message) {
	var params struct {
		Generation *uint64 `json:"generation"`
		SessionID  string  `json:"sessionId"`
		Model      string  `json:"model"`
	}
	if json.Unmarshal(incoming.Params, &params) != nil || params.Generation == nil {
		s.respondError(incoming.ID, -32602, "generation is required")
		return
	}
	current := s.suggestionSession(params.SessionID)
	if current == nil {
		s.respondPromptSuggestion(incoming.ID, *params.Generation, "")
		return
	}
	current.mu.Lock()
	if current.closed || current.runner == nil || current.suggestDone != nil {
		current.mu.Unlock()
		s.respondPromptSuggestion(incoming.ID, *params.Generation, "")
		return
	}
	ctx, cancel := context.WithTimeout(parent, suggestPromptTimeout)
	done := make(chan struct{})
	runner, cwd := current.runner, current.cwd
	current.suggestCancel, current.suggestDone = cancel, done
	current.mu.Unlock()

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer cancel()
		suggestion, err := runner.SuggestPrompt(ctx, cwd, params.Model)
		if err != nil {
			suggestion = ""
		}
		current.mu.Lock()
		if current.suggestDone == done {
			current.suggestCancel, current.suggestDone = nil, nil
		}
		close(done)
		current.mu.Unlock()
		s.respondPromptSuggestion(incoming.ID, *params.Generation, suggestion)
	}()
}

func (s *Server) suggestionSession(id string) *session {
	if id != "" {
		return s.lookupSession(id)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, current := range s.sessions {
		return current
	}
	return nil
}

func (s *Server) respondPromptSuggestion(id json.RawMessage, generation uint64, suggestion string) {
	var value any
	if suggestion != "" {
		value = suggestion
	}
	s.respond(id, map[string]any{"suggestion": value, "generation": generation})
}
