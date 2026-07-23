package acp

import (
	"context"
	"encoding/json"
	"errors"

	shareapp "github.com/lookcorner/go-cli/internal/share"
)

type shareRequest struct {
	SessionID string `json:"session_id"`
}

func (s *Server) handleShareSession(ctx context.Context, incoming message) {
	var request shareRequest
	if json.Unmarshal(incoming.Params, &request) != nil {
		s.respondErrorData(incoming.ID, -32602, "Invalid params", "session_id is required")
		return
	}
	config := s.authSnapshot()
	service := shareapp.Service{
		SessionDir: s.SessionDir, AuthPath: config.Path, AuthScope: config.Scope,
		HTTP: config.HTTP, TokenProvider: config.TokenProvider, Enabled: s.SharingEnabled,
	}
	url, err := service.Share(ctx, request.SessionID)
	if err != nil {
		s.respondShareError(incoming, err)
		return
	}
	s.respond(incoming.ID, map[string]any{"share_url": url})
}

func (s *Server) respondShareError(incoming message, err error) {
	var shareErr *shareapp.Error
	if !errors.As(err, &shareErr) {
		s.respondErrorData(incoming.ID, -32603, "Internal error", err.Error())
		return
	}
	switch shareErr.Kind {
	case shareapp.Authentication:
		s.respondErrorData(incoming.ID, -32000, "Authentication required", shareErr.Message)
	case shareapp.NotFound:
		s.respondErrorData(incoming.ID, -32002, "Session not found", shareErr.Message)
	case shareapp.Invalid:
		s.respondErrorData(incoming.ID, -32602, "Invalid params", shareErr.Message)
	default:
		s.respondErrorData(incoming.ID, -32603, "Internal error", shareErr.Message)
	}
}
