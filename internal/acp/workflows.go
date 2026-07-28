package acp

import (
	"encoding/json"

	"github.com/lookcorner/go-cli/internal/workflow"
)

func (s *Server) handleWorkflowsList(incoming message) {
	var req struct {
		SessionID string `json:"sessionId"`
	}
	if json.Unmarshal(incoming.Params, &req) != nil || req.SessionID == "" {
		s.respondError(incoming.ID, -32602, "sessionId is required")
		return
	}
	current := s.lookupSession(req.SessionID)
	if current == nil {
		s.respond(incoming.ID, map[string]any{
			"result": nil,
			"error":  "unknown session id: " + req.SessionID,
		})
		return
	}
	current.mu.Lock()
	cwd := current.cwd
	closed := current.closed
	current.mu.Unlock()
	if closed {
		s.respond(incoming.ID, map[string]any{
			"result": nil,
			"error":  "unknown session id: " + req.SessionID,
		})
		return
	}
	// Discovery-only: Go does not execute Rhai workflows yet, but ACP clients still
	// need the catalog. Always list when the session exists (Rust gates on launches).
	listings := workflow.List(cwd)
	items := make([]map[string]any, 0, len(listings))
	for _, item := range listings {
		row := map[string]any{
			"name":        item.Name,
			"description": item.Description,
			"source":      item.Source,
		}
		if item.WhenToUse != nil {
			row["when_to_use"] = *item.WhenToUse
		}
		if item.Path != nil {
			row["path"] = *item.Path
		}
		items = append(items, row)
	}
	s.respond(incoming.ID, map[string]any{"result": map[string]any{"workflows": items}})
}
