package acp

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/lookcorner/go-cli/internal/config"
	"github.com/lookcorner/go-cli/internal/workspace"
)

func (s *Server) handleMCPSetup(ctx context.Context, incoming message) {
	var req struct {
		SessionID       string            `json:"sessionId"`
		LegacySessionID string            `json:"session_id"`
		ServerName      string            `json:"serverName"`
		ServerNameSnake string            `json:"server_name"`
		Values          map[string]string `json:"values"`
	}
	if json.Unmarshal(incoming.Params, &req) != nil {
		s.respondError(incoming.ID, -32602, "invalid MCP setup parameters")
		return
	}
	if req.SessionID == "" {
		req.SessionID = req.LegacySessionID
	}
	if req.ServerName == "" {
		req.ServerName = req.ServerNameSnake
	}
	req.ServerName = strings.TrimSpace(req.ServerName)
	if req.SessionID == "" || req.ServerName == "" {
		s.respondError(incoming.ID, -32602, "sessionId and serverName are required")
		return
	}
	current := s.lookupSession(req.SessionID)
	if current == nil {
		s.respondError(incoming.ID, -32602, "session not found")
		return
	}
	current.mu.Lock()
	cwd, runner := current.cwd, current.runner
	current.mu.Unlock()
	if runner == nil {
		s.respondError(incoming.ID, -32602, "session not found")
		return
	}

	path, err := config.DefaultPath()
	if err != nil {
		s.respond(incoming.ID, map[string]any{"result": nil, "error": err.Error()})
		return
	}
	cfg, err := config.Load(path)
	if err != nil {
		s.respond(incoming.ID, map[string]any{"result": nil, "error": err.Error()})
		return
	}
	trusted := workspace.ResolveFolderTrust(cwd, cfg.FolderTrustEnabled, false) == workspace.TrustTrusted
	entries := config.CollectMCPSetupConfigs(cwd, cfg, nil, trusted)
	entry, ok := entries[req.ServerName]
	if !ok || entry.Config.Setup == nil {
		s.respondError(incoming.ID, -32602, "server setup not found")
		return
	}

	filtered := map[string]string{}
	for _, field := range entry.Config.Setup.Fields {
		if value, exists := req.Values[field.ID]; exists {
			filtered[field.ID] = value
		}
	}
	source := entry.Source
	pending := config.NewMCPServerPreferences(filtered, &source)
	switch resolved := entry.Config.ResolveSetup(&pending); resolved.Kind {
	case config.MCPSetupRequired:
		s.respondError(incoming.ID, -32602, "setup values incomplete")
		return
	case config.MCPSetupInvalid:
		s.respondError(incoming.ID, -32602, resolved.Reason)
		return
	}

	load := config.LoadMCPPreferences()
	if !load.Writable() {
		s.respond(incoming.ID, map[string]any{
			"result": nil,
			"error":  "MCP preferences file is unreadable; fix or remove mcp_preferences.json before saving",
		})
		return
	}
	prefs := load.File
	var previous *config.MCPServerPreferences
	if existing, exists := prefs.Servers[req.ServerName]; exists {
		copy := existing
		previous = &copy
	}
	prefs.Servers[req.ServerName] = pending
	if err := config.SaveMCPPreferences(prefs); err != nil {
		s.respond(incoming.ID, map[string]any{"result": nil, "error": err.Error()})
		return
	}
	rollback := func() { _ = config.RestoreMCPPreferenceServer(req.ServerName, previous) }

	if runner.ReloadMCPBase != nil {
		if err := runner.ReloadMCPBase(ctx); err != nil {
			rollback()
			s.respond(incoming.ID, map[string]any{"result": nil, "error": err.Error()})
			return
		}
	}
	if runner.ToggleMCPServer != nil {
		if err := runner.ToggleMCPServer(ctx, req.ServerName, true); err != nil {
			rollback()
			s.respond(incoming.ID, map[string]any{"result": nil, "error": err.Error()})
			return
		}
	}
	s.respond(incoming.ID, map[string]any{"result": map[string]any{"ok": true}, "error": nil})
}
