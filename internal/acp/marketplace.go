package acp

import (
	"context"
	"encoding/json"

	"github.com/lookcorner/go-cli/internal/marketplace"
)

func (s *Server) handleMarketplace(ctx context.Context, incoming message) {
	var base struct {
		SessionID string `json:"sessionId"`
	}
	_ = json.Unmarshal(incoming.Params, &base)
	current := s.lookupSession(base.SessionID)
	if current == nil || current.runner == nil {
		s.respondError(incoming.ID, -32602, "session not found")
		return
	}
	if incoming.Method == "x.ai/marketplace/list" {
		if current.runner.MarketplaceList == nil {
			s.respond(incoming.ID, map[string]any{"result": map[string]any{"sources": []any{}}, "error": nil})
			return
		}
		results, err := current.runner.MarketplaceList()
		if err != nil {
			s.respondError(incoming.ID, -32000, err.Error())
			return
		}
		s.respond(incoming.ID, map[string]any{"result": map[string]any{"sources": results}, "error": nil})
		return
	}
	if current.runner.MarketplaceAction == nil {
		s.respond(incoming.ID, map[string]any{"result": map[string]any{"status": "unsupported", "message": "Marketplace is unavailable."}, "error": nil})
		return
	}
	if s.anySessionRunning() {
		s.respond(incoming.ID, map[string]any{"result": map[string]any{
			"status": "validation_error", "message": "Cannot update marketplaces while a prompt is running.",
			"requiresReload": false, "requiresRestart": false,
		}, "error": nil})
		return
	}
	var req struct {
		SessionID string `json:"sessionId"`
		Action    struct {
			Type               string `json:"type"`
			SourceURLOrPath    string `json:"sourceUrlOrPath"`
			LegacySource       string `json:"source_url_or_path"`
			URL                string `json:"url"`
			PluginRelativePath string `json:"pluginRelativePath"`
			LegacyPluginPath   string `json:"plugin_relative_path"`
		} `json:"action"`
	}
	if err := json.Unmarshal(incoming.Params, &req); err != nil || req.SessionID == "" {
		s.respondError(incoming.ID, -32602, "sessionId is required")
		return
	}
	source := req.Action.SourceURLOrPath
	if source == "" {
		source = req.Action.LegacySource
	}
	if source == "" {
		source = req.Action.URL
	}
	path := req.Action.PluginRelativePath
	if path == "" {
		path = req.Action.LegacyPluginPath
	}
	outcome, err := current.runner.MarketplaceAction(ctx, marketplace.Action{Type: req.Action.Type, SourceURLOrPath: source, PluginRelativePath: path})
	if err != nil {
		s.respondError(incoming.ID, -32000, err.Error())
		return
	}
	s.respond(incoming.ID, map[string]any{"result": outcome, "error": nil})
}
