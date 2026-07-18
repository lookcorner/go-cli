package acp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"

	"github.com/lookcorner/go-cli/internal/plugin"
)

func (s *Server) handlePlugins(ctx context.Context, incoming message) {
	var req struct {
		SessionID string `json:"sessionId"`
		Action    struct {
			Type           string `json:"type"`
			Path           string `json:"path"`
			PluginID       string `json:"plugin_id"`
			LegacyPluginID string `json:"pluginId"`
		} `json:"action"`
	}
	if json.Unmarshal(incoming.Params, &req) != nil || req.SessionID == "" {
		s.respondError(incoming.ID, -32602, "sessionId is required")
		return
	}
	current := s.lookupSession(req.SessionID)
	if current == nil || current.runner == nil {
		s.respondError(incoming.ID, -32602, "session not found")
		return
	}
	if incoming.Method == "x.ai/plugins/action" {
		s.handlePluginAction(ctx, incoming, current, req.Action.Type, req.Action.Path, firstString(req.Action.PluginID, req.Action.LegacyPluginID))
		return
	}
	inventory := []plugin.Plugin{}
	if current.runner.PluginInventory != nil {
		inventory = current.runner.PluginInventory()
	}
	items := make([]map[string]any, 0, len(inventory))
	for _, item := range inventory {
		items = append(items, pluginWireInfo(item))
	}
	s.respond(incoming.ID, map[string]any{"result": map[string]any{"plugins": items}, "error": nil})
}

func (s *Server) handlePluginAction(ctx context.Context, incoming message, current *session, action, path, pluginID string) {
	if current.runner.UpdatePlugins == nil {
		s.pluginActionOutcome(incoming, "unsupported", "Plugin configuration is read-only.", false)
		return
	}
	if action == "install" || action == "uninstall" || action == "update" {
		s.pluginActionOutcome(incoming, "unsupported", "Plugin installation and updates are not implemented.", false)
		return
	}
	if action != "reload" && action != "add" && action != "remove" && action != "enable" && action != "disable" {
		s.pluginActionOutcome(incoming, "validation_error", "Unsupported plugin action.", false)
		return
	}
	if s.anySessionRunning() {
		s.pluginActionOutcome(incoming, "validation_error", "Cannot update plugins while a prompt is running.", false)
		return
	}
	resolved := ""
	if action == "add" || action == "remove" {
		if path == "" {
			s.pluginActionOutcome(incoming, "validation_error", "Path is required.", false)
			return
		}
		resolved = plugin.ResolvePath(path, current.cwd)
	}
	if (action == "enable" || action == "disable") && pluginID == "" {
		s.pluginActionOutcome(incoming, "validation_error", "Plugin ID is required.", false)
		return
	}
	before := []plugin.Plugin{}
	if current.runner.PluginInventory != nil {
		before = current.runner.PluginInventory()
	}
	var update func(*plugin.Settings)
	if action != "reload" {
		update = func(settings *plugin.Settings) {
			switch action {
			case "add":
				if !containsString(settings.Paths, resolved) {
					settings.Paths = append(settings.Paths, resolved)
				}
			case "remove":
				settings.Paths = removeString(settings.Paths, resolved)
			case "enable":
				if !containsString(settings.Enabled, pluginID) {
					settings.Enabled = append(settings.Enabled, pluginID)
				}
				settings.Disabled = removeString(settings.Disabled, pluginID)
			case "disable":
				if !containsString(settings.Disabled, pluginID) {
					settings.Disabled = append(settings.Disabled, pluginID)
				}
				settings.Enabled = removeString(settings.Enabled, pluginID)
			}
		}
	}
	after, err := current.runner.UpdatePlugins(ctx, update)
	if err != nil {
		s.pluginActionOutcome(incoming, "internal_error", err.Error(), false)
		return
	}
	restart := affectedPluginNeedsRestart(action, resolved, pluginID, before, after)
	message := "Plugins reloaded"
	switch action {
	case "add":
		message = "Added plugin path: " + resolved
	case "remove":
		message = "Removed plugin path: " + resolved
	case "enable":
		message = "Enabled: " + pluginID
	case "disable":
		message = "Disabled: " + pluginID
	}
	if restart {
		message += ". Restart to apply MCP/LSP changes."
	} else {
		message += "."
	}
	s.pluginActionOutcome(incoming, "success", message, restart)
}

func (s *Server) anySessionRunning() bool {
	s.mu.Lock()
	sessions := make([]*session, 0, len(s.sessions))
	for _, current := range s.sessions {
		sessions = append(sessions, current)
	}
	s.mu.Unlock()
	for _, current := range sessions {
		current.mu.Lock()
		running := current.running
		current.mu.Unlock()
		if running {
			return true
		}
	}
	return false
}

func (s *Server) pluginActionOutcome(incoming message, status, message string, restart bool) {
	s.respond(incoming.ID, map[string]any{"result": map[string]any{
		"status": status, "message": message, "requiresReload": false, "requiresRestart": restart,
	}, "error": nil})
}

func affectedPluginNeedsRestart(action, path, id string, before, after []plugin.Plugin) bool {
	if action == "reload" {
		for _, item := range after {
			if pluginNeedsRestart(item) {
				return true
			}
		}
		return false
	}
	for _, inventory := range [][]plugin.Plugin{before, after} {
		for _, item := range inventory {
			matches := (action == "add" || action == "remove") && item.Root == path ||
				(action == "enable" || action == "disable") && (item.ID == id || item.Name == id)
			if matches && pluginNeedsRestart(item) {
				return true
			}
		}
	}
	return false
}

func pluginNeedsRestart(item plugin.Plugin) bool {
	return item.LSPConfig != "" || len(item.InlineLSP) > 0
}

func firstString(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func pluginWireInfo(item plugin.Plugin) map[string]any {
	skillNames := pluginSkillNames(item.SkillDirs)
	mcpCount := pluginMCPServerCount(item)
	mcpStatus := "none"
	if mcpCount > 0 {
		mcpStatus = "active"
		if !item.Trusted {
			mcpStatus = "blocked"
		} else if item.MCPConfig == "" && len(item.InlineMCP) > 0 {
			mcpStatus = "active_inline"
		}
	}
	info := map[string]any{
		"name": item.Name, "id": item.ID, "root": item.Root, "scope": item.Scope,
		"trusted": item.Trusted, "enabled": item.Enabled,
		"version": pluginOptionalString(item.Version), "description": pluginOptionalString(item.Description),
		"skillCount": len(skillNames), "agentCount": 0, "hookStatus": "none", "hookCount": 0,
		"mcpServerCount": mcpCount, "mcpStatus": mcpStatus,
	}
	if len(skillNames) > 0 {
		info["skillNames"] = skillNames
	}
	return info
}

func pluginSkillNames(dirs []string) []string {
	seen := make(map[string]bool)
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() {
				seen[entry.Name()] = true
			}
		}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func pluginMCPServerCount(item plugin.Plugin) int {
	servers := make(map[string]bool)
	collectMCPServerNames(item.InlineMCP, servers)
	if item.MCPConfig != "" {
		if data, err := os.ReadFile(filepath.Clean(item.MCPConfig)); err == nil {
			collectMCPServerNames(data, servers)
		}
	}
	return len(servers)
}

func collectMCPServerNames(data []byte, servers map[string]bool) {
	var raw map[string]json.RawMessage
	if len(data) == 0 || json.Unmarshal(data, &raw) != nil {
		return
	}
	if nested, ok := raw["mcpServers"]; ok {
		var configured map[string]json.RawMessage
		if json.Unmarshal(nested, &configured) != nil {
			return
		}
		raw = configured
	}
	for name := range raw {
		servers[name] = true
	}
}

func pluginOptionalString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
