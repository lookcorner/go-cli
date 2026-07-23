package acp

import (
	"encoding/json"
	"fmt"
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
	commands := []map[string]any{
		availableCommand("compact", "Compress conversation history to save context window", "optional context about what to preserve", nil),
		availableCommand("context", "Show context window usage and session stats", "", nil),
		availableCommand("session-info", "Show session details (model, turns, context usage)", "", nil),
	}
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

func sessionStatusCommand(prompt string) string {
	fields := strings.Fields(prompt)
	if len(fields) == 0 {
		return ""
	}
	switch fields[0] {
	case "/session-info", "/status", "/info":
		return "session-info"
	case "/context":
		return "context"
	default:
		return ""
	}
}

func (s *Server) handleSessionStatusPrompt(incoming message, current *session, lifecycle promptLifecycle, command string) {
	current.mu.Lock()
	if current.closed {
		current.mu.Unlock()
		s.failPrompt(incoming, current, lifecycle, "session is closed")
		return
	}
	if current.running {
		current.mu.Unlock()
		s.failPrompt(incoming, current, lifecycle, "session already has an active prompt")
		return
	}
	id, cwd, title, turns := current.id, current.cwd, current.title, current.promptIndex
	used, total := current.inputTokens, current.runner.ContextWindow
	model := current.runner.ModelID
	if model == "" {
		model = current.runner.Model
	}
	current.mu.Unlock()

	if command == "session-info" {
		text := fmt.Sprintf("**Session ID:** %s\n\n**Working directory:** %s\n\n**Model:** %s\n\n**Turn:** %d\n\n**Context:** %d / %d tokens (%d%%)", id, cwd, model, turns, used, total, contextUsagePercent(used, total))
		if title != "" {
			text = "**Title:** " + title + "\n\n" + text
		}
		s.notify(id, map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": text}})
	}
	s.finishPrompt(incoming, current, lifecycle, "end_turn", agent.Result{}, nil, "")
}

func contextUsagePercent(used, total int) int {
	if total <= 0 {
		return 0
	}
	return used * 100 / total
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
