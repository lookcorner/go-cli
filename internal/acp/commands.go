package acp

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/skills"
)

func (s *Server) handleCommands(incoming message) {
	var req struct {
		CWD string `json:"cwd"`
	}
	if json.Unmarshal(incoming.Params, &req) != nil {
		s.respondError(incoming.ID, -32602, "invalid commands list parameters")
		return
	}
	runner := s.commandRunner(req.CWD)
	s.respond(incoming.ID, map[string]any{"commands": availableCommands(runner, req.CWD != "")})
}

func (s *Server) commandRunner(cwd string) *agent.Runner {
	s.mu.Lock()
	sessions := make([]*session, 0, len(s.sessions))
	for _, current := range s.sessions {
		if cwd == "" || current.cwd == cwd {
			sessions = append(sessions, current)
		}
	}
	s.mu.Unlock()
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].id < sessions[j].id })
	for _, current := range sessions {
		current.mu.Lock()
		runner, closed := current.runner, current.closed
		current.mu.Unlock()
		if !closed && runner != nil {
			return runner
		}
	}
	return nil
}

func availableCommands(runner *agent.Runner, workspaceSkills bool) []map[string]any {
	commands := []map[string]any{availableCommand("compact", "Compress conversation history to save context window", "optional context about what to preserve", nil)}
	if runner == nil {
		return commands
	}
	memoryConfigured, memoryEnabled := runner.MemoryAvailability()
	if memoryEnabled && runner.Tools != nil && runner.Tools.HasTool("memory_search") && runner.Tools.HasTool("memory_get") {
		commands = append(commands,
			availableCommand("flush", "Save reusable conversation context to workspace memory", "", nil),
			availableCommand("dream", "Consolidate session logs into organized memory", "", nil),
		)
	}
	if memoryConfigured {
		commands = append(commands, availableCommand("memory", "Browse or toggle workspace memory", "on|off", nil))
	}
	if runner.Tools != nil && runner.Tools.HasTool("scheduler_create") {
		commands = append(commands, availableCommand("loop", "Run a prompt on a recurring interval", "[interval] <prompt>", nil))
	}
	if runner.Skills == nil {
		return commands
	}
	items := runner.Skills.List()
	counts := make(map[string]int)
	builtin := make(map[string]bool, len(commands))
	for _, command := range commands {
		builtin[command["name"].(string)] = true
	}
	for _, item := range items {
		if item.Enabled && item.UserInvocable && (workspaceSkills || item.Scope == "user") {
			counts[item.Name]++
		}
	}
	sort.Slice(items, func(i, j int) bool {
		left, right := qualifiedCommandName(items[i]), qualifiedCommandName(items[j])
		if left == right {
			return items[i].Path < items[j].Path
		}
		return left < right
	})
	for _, item := range items {
		if !item.Enabled || !item.UserInvocable || !workspaceSkills && item.Scope != "user" {
			continue
		}
		name := item.Name
		if counts[name] > 1 || builtin[name] {
			name = qualifiedCommandName(item)
		}
		description := strings.TrimSpace(item.ShortDescription)
		if description == "" {
			description = item.Description
		}
		commands = append(commands, availableCommand(name, description, item.ArgumentHint, map[string]any{
			"scope": item.Scope,
			"path":  item.Path,
		}))
	}
	return commands
}

func qualifiedCommandName(item skills.Info) string {
	if item.PluginName != "" {
		return item.PluginName + ":" + item.Name
	}
	if item.Scope != "" {
		return item.Scope + ":" + item.Name
	}
	return item.Name
}

func availableCommand(name, description, hint string, meta map[string]any) map[string]any {
	command := map[string]any{"name": name, "description": description}
	if hint != "" {
		command["input"] = map[string]any{"hint": hint}
	}
	if len(meta) > 0 {
		command["_meta"] = meta
	}
	return command
}
