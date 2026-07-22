package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lookcorner/go-cli/internal/agents"
	"github.com/lookcorner/go-cli/internal/hooks"
	"github.com/lookcorner/go-cli/internal/plugin"
)

func (s *Server) handlePlugins(ctx context.Context, incoming message) {
	if incoming.Method == "x.ai/plugins/reload" {
		s.handlePluginReload(ctx, incoming)
		return
	}
	if incoming.Method == "x.ai/plugins/notify-updates" {
		s.handlePluginUpdates(incoming)
		return
	}
	var req struct {
		SessionID string `json:"sessionId"`
		Action    struct {
			Type           string `json:"type"`
			Path           string `json:"path"`
			Source         string `json:"source"`
			PluginID       string `json:"plugin_id"`
			LegacyPluginID string `json:"pluginId"`
			Confirmed      bool   `json:"confirmed"`
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
		s.handlePluginAction(ctx, incoming, current, req.Action.Type, req.Action.Path, req.Action.Source, firstString(req.Action.PluginID, req.Action.LegacyPluginID), req.Action.Confirmed)
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

func (s *Server) handlePluginUpdates(incoming message) {
	var req struct {
		SessionID *string     `json:"sessionId"`
		Updates   *[][]string `json:"updates"`
	}
	if json.Unmarshal(incoming.Params, &req) != nil || req.SessionID == nil || req.Updates == nil {
		s.respondError(incoming.ID, -32602, "invalid plugin update notification")
		return
	}
	for _, update := range *req.Updates {
		if len(update) != 3 {
			s.respondError(incoming.ID, -32602, "invalid plugin update notification")
			return
		}
	}
	if current := s.lookupSession(*req.SessionID); current != nil {
		s.notifyXAI(current, map[string]any{"sessionUpdate": "plugin_updates_installed", "updates": *req.Updates})
	}
	s.respond(incoming.ID, map[string]any{"ok": true})
}

func (s *Server) handlePluginReload(ctx context.Context, incoming message) {
	_ = plugin.RefreshLocal()
	s.mu.Lock()
	sessions := make([]*session, 0, len(s.sessions))
	for _, current := range s.sessions {
		sessions = append(sessions, current)
	}
	s.mu.Unlock()
	for _, current := range sessions {
		if current != nil && current.runner != nil && current.runner.UpdatePlugins != nil {
			_, _ = current.runner.UpdatePlugins(ctx, nil)
			break
		}
	}
	s.respond(incoming.ID, map[string]any{"ok": true})
}

func (s *Server) handlePluginAction(ctx context.Context, incoming message, current *session, action, path, source, pluginID string, confirmed bool) {
	if current.runner.UpdatePlugins == nil {
		s.pluginActionOutcome(incoming, "unsupported", "Plugin configuration is read-only.", false, false)
		return
	}
	if action != "reload" && action != "install" && action != "uninstall" && action != "update" && action != "add" && action != "remove" && action != "enable" && action != "disable" {
		s.pluginActionOutcome(incoming, "validation_error", "Unsupported plugin action.", false, false)
		return
	}
	if s.anySessionRunning() {
		s.pluginActionOutcome(incoming, "validation_error", "Cannot update plugins while a prompt is running.", false, false)
		return
	}
	if action == "install" {
		s.installPlugin(ctx, incoming, current, source)
		return
	}
	if action == "uninstall" {
		s.uninstallPlugin(ctx, incoming, current, pluginID, confirmed)
		return
	}
	if action == "update" {
		s.updatePlugins(ctx, incoming, current, pluginID)
		return
	}
	resolved := ""
	if action == "add" || action == "remove" {
		if path == "" {
			s.pluginActionOutcome(incoming, "validation_error", "Path is required.", false, false)
			return
		}
		resolved = plugin.ResolvePath(path, current.cwd)
	}
	if (action == "enable" || action == "disable") && pluginID == "" {
		s.pluginActionOutcome(incoming, "validation_error", "Plugin ID is required.", false, false)
		return
	}
	var update func(*plugin.Settings)
	if action == "reload" {
		_ = plugin.RefreshLocal()
	}
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
	_, err := current.runner.UpdatePlugins(ctx, update)
	if err != nil {
		s.pluginActionOutcome(incoming, "internal_error", err.Error(), false, false)
		return
	}
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
	s.pluginActionOutcome(incoming, "success", message+".", false, false)
}

func (s *Server) installPlugin(ctx context.Context, incoming message, current *session, source string) {
	if source == "" {
		s.pluginActionOutcome(incoming, "validation_error", "Source is required (git URL or local path).", false, false)
		return
	}
	outcome, err := plugin.Install(source, current.cwd)
	if err != nil {
		s.pluginActionOutcome(incoming, "internal_error", "Failed to install plugin: "+err.Error(), false, false)
		return
	}
	_, err = current.runner.UpdatePlugins(ctx, func(settings *plugin.Settings) {
		for _, name := range outcome.Plugins {
			if !containsString(settings.Enabled, name) {
				settings.Enabled = append(settings.Enabled, name)
			}
			settings.Disabled = removeString(settings.Disabled, name)
		}
	})
	if err != nil {
		s.pluginActionOutcome(incoming, "internal_error", err.Error(), false, false)
		return
	}
	s.pluginActionOutcome(incoming, "success", fmt.Sprintf("Installed %d plugin(s) from %s: %s", len(outcome.Plugins), source, strings.Join(outcome.Plugins, ", ")), false, false)
}

func (s *Server) uninstallPlugin(ctx context.Context, incoming message, current *session, pluginID string, confirmed bool) {
	if pluginID == "" {
		s.pluginActionOutcome(incoming, "validation_error", "Plugin ID is required.", false, false)
		return
	}
	outcome, err := plugin.Uninstall(pluginID, confirmed, false)
	if err != nil {
		var confirmation *plugin.ConfirmationError
		var missing *plugin.NotFoundError
		switch {
		case errors.As(err, &confirmation):
			s.pluginActionOutcome(incoming, "confirmation_required", confirmation.Error()+". Uninstalling removes all of them.", false, false)
		case errors.As(err, &missing):
			s.pluginActionOutcome(incoming, "not_found", err.Error(), false, false)
		default:
			s.pluginActionOutcome(incoming, "internal_error", err.Error(), false, false)
		}
		return
	}
	_, err = current.runner.UpdatePlugins(ctx, func(settings *plugin.Settings) {
		for _, name := range outcome.Plugins {
			settings.Enabled = removeString(settings.Enabled, name)
			settings.Disabled = removeString(settings.Disabled, name)
		}
	})
	if err != nil {
		s.pluginActionOutcome(incoming, "internal_error", err.Error(), false, false)
		return
	}
	s.pluginActionOutcome(incoming, "success", fmt.Sprintf("Uninstalled repo %q (%d plugin(s): %s)", outcome.RepoKey, len(outcome.Plugins), strings.Join(outcome.Plugins, ", ")), false, false)
}

func (s *Server) updatePlugins(ctx context.Context, incoming message, current *session, pluginID string) {
	outcomes, err := plugin.Update(pluginID)
	if err != nil {
		var missing *plugin.NotFoundError
		if errors.As(err, &missing) || strings.Contains(err.Error(), "no installed plugins") {
			s.pluginActionOutcome(incoming, "not_found", err.Error(), false, false)
		} else {
			s.pluginActionOutcome(incoming, "internal_error", err.Error(), false, false)
		}
		return
	}
	if _, err := current.runner.UpdatePlugins(ctx, nil); err != nil {
		s.pluginActionOutcome(incoming, "internal_error", err.Error(), false, false)
		return
	}
	messages := make([]string, 0, len(outcomes))
	for _, outcome := range outcomes {
		messages = append(messages, outcome.RepoKey+": "+strings.ReplaceAll(outcome.Status, "_", " "))
	}
	s.pluginActionOutcome(incoming, "success", strings.Join(messages, "; "), false, false)
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

func (s *Server) pluginActionOutcome(incoming message, status, message string, reload, restart bool) {
	s.respond(incoming.ID, map[string]any{"result": map[string]any{
		"status": status, "message": message, "requiresReload": reload, "requiresRestart": restart,
	}, "error": nil})
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
	agentNames := agents.PluginNames(item)
	hookCount := hooks.CountDefined(item)
	hookStatus := "none"
	if item.HooksConfig != "" || len(item.InlineHooks) > 0 {
		hookStatus = "active"
		if !item.Executable {
			hookStatus = "blocked"
		} else if item.HooksConfig == "" && len(item.InlineHooks) > 0 {
			hookStatus = "active_inline"
		}
	}
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
		"skillCount": len(skillNames), "agentCount": len(agentNames), "hookStatus": hookStatus, "hookCount": hookCount,
		"mcpServerCount": mcpCount, "mcpStatus": mcpStatus,
	}
	if len(skillNames) > 0 {
		info["skillNames"] = skillNames
	}
	if len(agentNames) > 0 {
		info["agentNames"] = agentNames
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
