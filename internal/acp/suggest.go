package acp

import (
	"context"
	"encoding/json"
	"time"

	sessionlog "github.com/lookcorner/go-cli/internal/session"
	commandsuggest "github.com/lookcorner/go-cli/internal/suggest"
)

const suggestPromptTimeout = 45 * time.Second
const shellSuggestTimeout = 2 * time.Second

func (s *Server) handleSuggest(parent context.Context, incoming message) {
	var params struct {
		Text       *string `json:"text"`
		Cursor     *int    `json:"cursor"`
		CWD        *string `json:"cwd"`
		Limit      *int    `json:"limit"`
		Generation *uint64 `json:"generation"`
		IncludeAI  bool    `json:"includeAi"`
		AIModel    string  `json:"aiModel"`
		SessionID  string  `json:"sessionId"`
		TokenOnly  bool    `json:"tokenOnly"`
	}
	if json.Unmarshal(incoming.Params, &params) != nil || params.Text == nil || params.Cursor == nil || params.CWD == nil || params.Limit == nil || params.Generation == nil || *params.Cursor < 0 || *params.Limit < 0 {
		s.respondError(incoming.ID, -32602, "text, cursor, cwd, limit, and generation are required")
		return
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		history, _ := sessionlog.PromptHistory(s.SessionDir, *params.CWD, "", true)
		ctx, cancel := context.WithTimeout(parent, shellSuggestTimeout)
		defer cancel()
		response := commandsuggest.Generate(ctx, commandsuggest.Request{
			Text: *params.Text, Cursor: *params.Cursor, CWD: *params.CWD, Limit: *params.Limit,
			Generation: *params.Generation, IncludeAI: params.IncludeAI, AIModel: params.AIModel, TokenOnly: params.TokenOnly,
		}, history, s.shellAICompleter(params.SessionID))
		s.respond(incoming.ID, response)
	}()
}

func (s *Server) shellAICompleter(sessionID string) commandsuggest.AICompleter {
	return func(ctx context.Context, prefix, cwd, model string) (string, error) {
		current := s.suggestionSession(sessionID)
		if current == nil {
			return "", context.Canceled
		}
		current.mu.Lock()
		if current.closed || current.runner == nil || current.suggestDone != nil {
			current.mu.Unlock()
			return "", context.Canceled
		}
		done := make(chan struct{})
		child, cancel := context.WithCancel(ctx)
		current.suggestCancel, current.suggestDone = cancel, done
		runner := current.runner
		current.mu.Unlock()
		defer func() {
			cancel()
			current.mu.Lock()
			if current.suggestDone == done {
				current.suggestCancel, current.suggestDone = nil, nil
			}
			close(done)
			current.mu.Unlock()
		}()
		return runner.SuggestShell(child, prefix, cwd, model)
	}
}

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
