package acp

import (
	"context"
	"encoding/json"
	"time"
)

const (
	minAutoRecapTurns = 3
	minAutoRecapIdle  = 3 * time.Minute
)

func (s *Server) handleRecap(parent context.Context, incoming message) {
	var params struct {
		SessionID string `json:"sessionId"`
		Auto      bool   `json:"auto"`
	}
	if json.Unmarshal(incoming.Params, &params) != nil || params.SessionID == "" {
		s.respondError(incoming.ID, -32602, "sessionId is required")
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
	turns := current.promptIndex
	eligible := turns > 0
	if params.Auto {
		eligible = !current.running && turns >= minAutoRecapTurns && turns > current.lastRecapPrompt && !current.updated.IsZero() && time.Since(current.updated) >= minAutoRecapIdle
	}
	if !eligible || current.recapDone != nil {
		current.mu.Unlock()
		s.ackRecap(incoming)
		if !params.Auto {
			s.notifyRecapUnavailable(params.SessionID)
		}
		return
	}
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	previous := current.previous
	current.recapCancel, current.recapDone = cancel, done
	current.mu.Unlock()

	s.ackRecap(incoming)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer cancel()
		summary, err := current.runner.Recap(ctx, previous)

		current.mu.Lock()
		notifySuccess := err == nil && !current.closed
		notifyUnavailable := err != nil && !current.closed && !params.Auto
		if notifySuccess {
			current.lastRecapPrompt = turns
		}
		current.mu.Unlock()

		if notifySuccess {
			s.notify(params.SessionID, map[string]any{
				"sessionUpdate": "session_recap",
				"summary":       summary,
				"auto":          params.Auto,
			})
		} else if notifyUnavailable {
			s.notifyRecapUnavailable(params.SessionID)
		}

		current.mu.Lock()
		current.recapCancel, current.recapDone = nil, nil
		close(done)
		current.mu.Unlock()
	}()
}

func (s *Server) ackRecap(incoming message) {
	s.respond(incoming.ID, map[string]any{"result": map[string]any{"ok": true}})
}

func (s *Server) notifyRecapUnavailable(sessionID string) {
	s.notify(sessionID, map[string]any{"sessionUpdate": "session_recap_unavailable"})
}
