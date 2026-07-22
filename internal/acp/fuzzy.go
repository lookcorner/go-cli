package acp

import (
	"context"
	"encoding/json"
	"path/filepath"
	"time"

	"github.com/lookcorner/go-cli/internal/workspace"
)

func (s *Server) handleFuzzySearch(ctx context.Context, incoming message) {
	switch incoming.Method {
	case "x.ai/search/fuzzy/open":
		var req struct {
			SessionID string  `json:"sessionId"`
			CWD       string  `json:"cwd"`
			Root      string  `json:"root"`
			RequestID *string `json:"requestId"`
			Hidden    bool    `json:"hidden"`
			Meta      struct {
				ClientID *struct {
					InstanceID *string `json:"instanceId"`
					ConnID     *string `json:"connId"`
				} `json:"clientId"`
			} `json:"_meta"`
		}
		if json.Unmarshal(incoming.Params, &req) != nil {
			s.respondError(incoming.ID, -32602, "invalid fuzzy search parameters")
			return
		}
		cwd, err := s.resolveSearchCWD(req.CWD, req.SessionID)
		if err != nil {
			s.respondError(incoming.ID, -32602, err.Error())
			return
		}
		root := cwd
		if req.Root != "" {
			if filepath.IsAbs(req.Root) {
				root = req.Root
			} else {
				root = filepath.Join(cwd, req.Root)
			}
		}
		routing := workspace.FuzzyRouting{SessionID: req.SessionID}
		if req.Meta.ClientID != nil {
			if req.Meta.ClientID.InstanceID == nil || req.Meta.ClientID.ConnID == nil {
				s.respondError(incoming.ID, -32602, "invalid fuzzy search clientId")
				return
			}
			routing.TargetClientID = &workspace.FuzzyClientID{InstanceID: *req.Meta.ClientID.InstanceID, ConnID: *req.Meta.ClientID.ConnID}
		}
		searchID, err := s.fuzzyManager().Open(root, req.RequestID, req.Hidden, routing)
		if err != nil {
			s.respondError(incoming.ID, -32000, err.Error())
			return
		}
		sessionID := req.SessionID
		if sessionID == "" {
			sessionID = "agent"
		}
		s.respond(incoming.ID, map[string]any{"result": map[string]any{"sessionId": sessionID, "searchId": searchID}, "error": nil})

	case "x.ai/search/fuzzy/change":
		var req struct {
			SearchID *string `json:"searchId"`
			Query    *string `json:"query"`
			DirsOnly bool    `json:"dirsOnly"`
			Limit    *int    `json:"limit"`
		}
		if json.Unmarshal(incoming.Params, &req) != nil || req.SearchID == nil || req.Query == nil || req.Limit != nil && *req.Limit < 0 {
			s.respondError(incoming.ID, -32602, "invalid fuzzy search parameters")
			return
		}
		limit := 100
		if req.Limit != nil {
			limit = *req.Limit
		}
		found := s.fuzzyManager().Change(ctx, *req.SearchID, *req.Query, req.DirsOnly, limit, func(status workspace.FuzzyStatus) {
			if s.closing.Load() {
				return
			}
			sessionID := status.Routing.SessionID
			if sessionID == "" {
				sessionID = "agent"
			}
			params := map[string]any{
				"sessionId": sessionID, "searchId": status.SearchID, "matches": status.Matches,
				"total": status.Total, "done": status.Done, "generation": status.Generation,
			}
			if status.Routing.TargetClientID != nil {
				params["_meta"] = map[string]any{"targetClientId": status.Routing.TargetClientID}
			}
			s.write(map[string]any{"jsonrpc": "2.0", "method": "x.ai/search/fuzzy/status", "params": params})
		})
		if !found {
			s.respondError(incoming.ID, -32602, "search not found: "+*req.SearchID)
			return
		}
		s.respond(incoming.ID, map[string]any{"result": map[string]any{"sessionId": "agent", "searchId": *req.SearchID}, "error": nil})

	case "x.ai/search/fuzzy/close":
		var req struct {
			SearchID *string `json:"searchId"`
		}
		if json.Unmarshal(incoming.Params, &req) != nil || req.SearchID == nil {
			s.respondError(incoming.ID, -32602, "searchId is required")
			return
		}
		closed := s.fuzzyManager().Close(*req.SearchID)
		s.respond(incoming.ID, map[string]any{"result": map[string]any{"sessionId": "agent", "searchId": *req.SearchID, "closed": closed}, "error": nil})
	}
}

func (s *Server) fuzzyManager() *workspace.FuzzySearchManager {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fuzzySearch == nil {
		s.fuzzySearch = workspace.NewFuzzySearchManager(5 * time.Minute)
	}
	return s.fuzzySearch
}
