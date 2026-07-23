package acp

import (
	"context"
	"encoding/json"

	"github.com/lookcorner/go-cli/internal/bundle"
)

type BundleConfig struct {
	Status func() (bundle.Status, error)
	Entry  func(kind, name string) (bundle.Entry, error)
	Sync   func(context.Context, bool) (bundle.SyncResult, error)
}

func (s *Server) handleBundle(ctx context.Context, incoming message) {
	switch incoming.Method {
	case "x.ai/bundle/status":
		if s.Bundle.Status == nil {
			s.respondError(incoming.ID, -32000, "bundle status is unavailable")
			return
		}
		var request struct{}
		if len(incoming.Params) > 0 && json.Unmarshal(incoming.Params, &request) != nil {
			s.respondError(incoming.ID, -32602, "invalid bundle status parameters")
			return
		}
		result, err := s.Bundle.Status()
		if err != nil {
			s.respondError(incoming.ID, -32000, err.Error())
			return
		}
		s.respond(incoming.ID, result)
	case "x.ai/bundle/entry/get":
		if s.Bundle.Entry == nil {
			s.respondError(incoming.ID, -32000, "bundle entries are unavailable")
			return
		}
		var request struct {
			Kind string `json:"kind"`
			Name string `json:"name"`
		}
		if json.Unmarshal(incoming.Params, &request) != nil || request.Kind == "" || request.Name == "" {
			s.respondError(incoming.ID, -32602, "kind and name are required")
			return
		}
		result, err := s.Bundle.Entry(request.Kind, request.Name)
		if err != nil {
			s.respondError(incoming.ID, -32000, err.Error())
			return
		}
		s.respond(incoming.ID, result)
	case "x.ai/bundle/sync":
		if s.Bundle.Sync == nil {
			s.respondError(incoming.ID, -32000, "bundle sync is unavailable")
			return
		}
		var request struct {
			Force bool `json:"force"`
		}
		if json.Unmarshal(incoming.Params, &request) != nil {
			s.respondError(incoming.ID, -32602, "invalid bundle sync parameters")
			return
		}
		result, err := s.Bundle.Sync(ctx, request.Force)
		if err != nil {
			s.respondError(incoming.ID, -32000, err.Error())
			return
		}
		s.respond(incoming.ID, result)
	}
}
