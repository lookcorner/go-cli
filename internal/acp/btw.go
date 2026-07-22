package acp

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

func (s *Server) handleBtw(parent context.Context, incoming message) {
	var params struct {
		SessionID string  `json:"sessionId"`
		Question  *string `json:"question"`
	}
	if json.Unmarshal(incoming.Params, &params) != nil || params.SessionID == "" || params.Question == nil {
		s.respondError(incoming.ID, -32602, "sessionId and question are required")
		return
	}
	current := s.lookupSession(params.SessionID)
	if current == nil {
		s.respondError(incoming.ID, -32602, "session not found: "+params.SessionID)
		return
	}
	current.mu.Lock()
	if current.closed {
		current.mu.Unlock()
		s.respondError(incoming.ID, -32603, "session is closed")
		return
	}
	if current.btwDone != nil {
		current.mu.Unlock()
		s.respondError(incoming.ID, -32603, "side question already in progress")
		return
	}
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	previous := current.previous
	current.btwCancel, current.btwDone, current.updated = cancel, done, time.Now().UTC()
	current.mu.Unlock()

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer cancel()
		answer, err := current.runner.SideQuestion(ctx, strings.TrimSpace(*params.Question), previous)
		current.mu.Lock()
		current.btwCancel, current.btwDone, current.updated = nil, nil, time.Now().UTC()
		close(done)
		current.mu.Unlock()
		if err != nil {
			s.respondError(incoming.ID, -32603, err.Error())
			return
		}
		s.respond(incoming.ID, map[string]any{"result": map[string]any{"answer": answer}})
	}()
}
