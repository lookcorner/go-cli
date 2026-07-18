package acp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"

	"github.com/lookcorner/go-cli/internal/plugin"
)

func (s *Server) handlePlugins(incoming message) {
	var req struct {
		SessionID string `json:"sessionId"`
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
	items := make([]map[string]any, 0, len(current.runner.Plugins))
	for _, item := range current.runner.Plugins {
		items = append(items, pluginWireInfo(item))
	}
	s.respond(incoming.ID, map[string]any{"result": map[string]any{"plugins": items}, "error": nil})
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
