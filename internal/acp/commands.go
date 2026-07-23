package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/billing"
	"github.com/lookcorner/go-cli/internal/changelog"
	"github.com/lookcorner/go-cli/internal/skills"
	"github.com/lookcorner/go-cli/internal/tools"
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
		availableCommand("always-approve", "Toggle always-approve mode (skip all permission prompts)", "on|off", nil),
		availableCommand("privacy", "Show privacy status (coding data retention is locked to opt-out)", "opt-out", nil),
		availableCommand("terminal-setup", "Check terminal, color, and clipboard setup", "", nil),
		availableCommand("usage", "View credit usage or manage billing", "show | manage", nil),
		availableCommand("release-notes", "View release notes for the current version", "", nil),
	}
	if runner != nil {
		if runner.SharingEnabled != nil && runner.SharingEnabled() {
			commands = append(commands, availableCommand("share", "Share this session via URL", "", nil))
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
		if runner.MCPServerCatalog != nil {
			commands = append(commands, availableCommand("mcps", "View MCP servers and tools", "", nil))
		}
	}
	commands = append(commands, availableCommand("context", "Show context window usage and session stats", "", nil))
	if runner != nil && runner.HookCatalog != nil {
		commands = append(commands,
			availableCommand("hooks-trust", "Trust this project for hook execution", "", nil),
			availableCommand("hooks-list", "Show hooks loaded in this session", "", nil),
			availableCommand("hooks-add", "Add a custom hook file or directory", "path to hook file or directory", nil),
			availableCommand("hooks-remove", "Remove a custom hook file or directory path", "path to hook file or directory", nil),
			availableCommand("hooks-untrust", "Remove trust for the current project", "", nil),
		)
	}
	if runner != nil && runner.PluginInventory != nil {
		commands = append(commands,
			availableCommand("plugins", "Manage plugins (list, reload, trust, add, remove)", "list | reload | trust <path> | add <path> | remove <path>", nil),
			availableCommand("reload-plugins", "Reload plugins from disk (alias for /plugins reload)", "", nil),
		)
	}
	commands = append(commands, availableCommand("session-info", "Show session details (model, turns, context usage)", "", nil))
	if runner == nil {
		return commands
	}
	if runner.SubmitFeedback != nil {
		commands = append(commands, availableCommand("feedback", "Send feedback about the current session", "feedback text", nil))
	}
	if runner.Tools != nil && runner.Tools.GoalAvailable() {
		commands = append(commands, availableCommand("goal", "Set, manage, or check an autonomous goal", "<objective> [--budget <tokens>] | status | pause | resume | clear", nil))
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

type goalCommand struct {
	action    string
	objective string
	budget    int64
}

type pluginCommand struct {
	action  string
	value   string
	confirm bool
}

func parsePluginCommand(prompt string) (pluginCommand, bool) {
	trimmed := strings.TrimSpace(prompt)
	if trimmed == "/reload-plugins" {
		return pluginCommand{action: "reload"}, true
	}
	name := ""
	for _, candidate := range []string{"/plugins", "/plugin"} {
		if trimmed == candidate || strings.HasPrefix(trimmed, candidate+" ") {
			name = candidate
			break
		}
	}
	if name == "" {
		return pluginCommand{}, false
	}
	args := strings.TrimSpace(strings.TrimPrefix(trimmed, name))
	switch {
	case args == "", args == "list":
		return pluginCommand{action: "list"}, true
	case args == "reload":
		return pluginCommand{action: "reload"}, true
	case strings.HasPrefix(args, "trust"):
		return pluginCommand{action: "trust"}, true
	case strings.HasPrefix(args, "add "):
		return pluginCommand{action: "add", value: strings.TrimSpace(strings.TrimPrefix(args, "add "))}, true
	case strings.HasPrefix(args, "remove "):
		return pluginCommand{action: "remove", value: strings.TrimSpace(strings.TrimPrefix(args, "remove "))}, true
	case strings.HasPrefix(args, "install "):
		value := strings.TrimSpace(strings.TrimPrefix(args, "install "))
		value, confirm := trimPluginFlag(value, "--trust")
		return pluginCommand{action: "install", value: value, confirm: confirm}, true
	case strings.HasPrefix(args, "uninstall "):
		value := strings.TrimSpace(strings.TrimPrefix(args, "uninstall "))
		value, confirm := trimPluginFlag(value, "--confirm")
		return pluginCommand{action: "uninstall", value: value, confirm: confirm}, true
	case args == "update":
		return pluginCommand{action: "update"}, true
	case strings.HasPrefix(args, "update "):
		return pluginCommand{action: "update", value: strings.TrimSpace(strings.TrimPrefix(args, "update "))}, true
	default:
		return pluginCommand{action: "list"}, true
	}
}

func trimPluginFlag(value, flag string) (string, bool) {
	if value == flag {
		return value, true
	}
	suffix := " " + flag
	if strings.HasSuffix(value, suffix) {
		return strings.TrimSpace(strings.TrimSuffix(value, suffix)), true
	}
	return value, false
}

func parseGoalCommand(prompt string) (goalCommand, bool) {
	trimmed := strings.TrimSpace(prompt)
	if !strings.HasPrefix(trimmed, "/goal") {
		return goalCommand{}, false
	}
	rest := strings.TrimPrefix(trimmed, "/goal")
	if rest != "" && strings.TrimLeftFunc(rest, unicode.IsSpace) == rest {
		return goalCommand{}, false
	}
	args := strings.TrimSpace(rest)
	switch strings.ToLower(args) {
	case "", "status":
		return goalCommand{action: "status"}, true
	case "pause", "resume", "clear":
		return goalCommand{action: strings.ToLower(args)}, true
	}
	objective, budget := parseGoalBudget(args)
	return goalCommand{action: "set", objective: objective, budget: budget}, true
}

func parseHookCommand(prompt string) (string, string, bool) {
	trimmed := strings.TrimSpace(prompt)
	fields := strings.Fields(trimmed)
	if len(fields) == 0 {
		return "", "", false
	}
	switch fields[0] {
	case "/hooks-trust", "/hooks-list", "/hooks-add", "/hooks-remove", "/hooks-untrust":
		return strings.TrimPrefix(fields[0], "/hooks-"), strings.TrimSpace(strings.TrimPrefix(trimmed, fields[0])), true
	default:
		return "", "", false
	}
}

func parseFeedbackCommand(prompt string) (string, bool) {
	trimmed := strings.TrimSpace(prompt)
	if trimmed == "/feedback" {
		return "", true
	}
	if !strings.HasPrefix(trimmed, "/feedback ") {
		return "", false
	}
	return strings.TrimSpace(strings.TrimPrefix(trimmed, "/feedback ")), true
}

func parseGoalBudget(objective string) (string, int64) {
	index := strings.LastIndex(objective, "--budget")
	if index <= 0 {
		return objective, 0
	}
	head := objective[:index]
	if strings.TrimRightFunc(head, unicode.IsSpace) == head {
		return objective, 0
	}
	tail := objective[index+len("--budget"):]
	if tail == "" || strings.TrimLeftFunc(tail, unicode.IsSpace) == tail {
		return objective, 0
	}
	value := strings.TrimSpace(tail)
	if value == "" || strings.IndexFunc(value, unicode.IsSpace) >= 0 {
		return objective, 0
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return objective, 0
		}
	}
	budget, err := strconv.ParseInt(value, 10, 64)
	if err != nil || budget <= 0 {
		return objective, 0
	}
	return strings.TrimSpace(head), budget
}

func goalStatusText(snapshot tools.GoalSnapshot) string {
	if snapshot.Objective == "" {
		return "No goal is currently set. Use /goal <objective> to start one."
	}
	elapsed := time.Duration(max(int64(0), time.Now().Unix()-snapshot.CreatedAtUnix)) * time.Second
	text := fmt.Sprintf("Goal: %s\nStatus: %s | Phase: %s\nTokens used: %d\nElapsed: %s", snapshot.Objective, goalStatusLabel(snapshot.Status), goalPhaseLabel(snapshot.Status), snapshot.TokensUsed, formatGoalElapsed(elapsed))
	if snapshot.TokenBudget > 0 {
		text += fmt.Sprintf(" | Budget: %d", snapshot.TokenBudget)
	}
	if snapshot.CurrentSubagentRole != "" {
		text += "\nActive subagent: " + snapshot.CurrentSubagentRole
	}
	return text
}

func goalStatusLabel(status string) string {
	labels := map[string]string{"active": "Active", "verifying": "Active", "user_paused": "UserPaused", "back_off_paused": "BackOffPaused", "no_progress_paused": "NoProgressPaused", "infra_paused": "InfraPaused", "blocked": "Blocked", "completed": "Complete", "budget_limited": "BudgetLimited"}
	if label := labels[status]; label != "" {
		return label
	}
	return status
}

func goalPhaseLabel(status string) string {
	if status == "active" {
		return "Executing"
	}
	if status == "verifying" {
		return "Verifying"
	}
	return "Idle"
}

func formatGoalElapsed(elapsed time.Duration) string {
	total := int64(elapsed / time.Second)
	if total >= 3600 {
		return fmt.Sprintf("%dh%02dm", total/3600, total%3600/60)
	}
	if total >= 60 {
		return fmt.Sprintf("%dm%02ds", total/60, total%60)
	}
	return fmt.Sprintf("%ds", total)
}

func (s *Server) sendCommandOutput(sessionID, text string) {
	s.notify(sessionID, map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": text}})
}

func alwaysApproveCommand(prompt string) (bool, bool) {
	trimmed := strings.TrimSpace(prompt)
	fields := strings.Fields(trimmed)
	if len(fields) == 0 || fields[0] != "/always-approve" && fields[0] != "/yolo" {
		return false, false
	}
	args := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(trimmed, fields[0])))
	switch args {
	case "off", "false", "0", "no", "disable":
		return false, true
	default:
		return true, true
	}
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

func (s *Server) handleLocalMessagePrompt(incoming message, current *session, lifecycle promptLifecycle, message string) {
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
	id := current.id
	current.mu.Unlock()

	s.sendCommandOutput(id, message)
	s.finishPrompt(incoming, current, lifecycle, "end_turn", agent.Result{}, nil, "")
}

func mcpStatusMessage(current *session) string {
	servers := mcpServerCatalog(current)
	if len(servers) == 0 {
		return "No MCP servers configured."
	}
	lines := []string{"# MCP servers", ""}
	for _, server := range servers {
		name, _ := server["name"].(string)
		session, _ := server["session"].(map[string]any)
		enabled, _ := session["enabled"].(bool)
		status := "disabled"
		if enabled {
			status = "enabled"
		}
		source, _ := server["url"].(string)
		if source == "" {
			source, _ = server["command"].(string)
		}
		tools, _ := session["tools"].([]map[string]any)
		enabledTools := 0
		for _, tool := range tools {
			if active, _ := tool["enabled"].(bool); active {
				enabledTools++
			}
		}
		lines = append(lines, fmt.Sprintf("- `%s` - %s - %d/%d tools - `%s`", acpMarkdownText(name), status, enabledTools, len(tools), acpMarkdownText(source)))
	}
	return strings.Join(lines, "\n")
}

func acpMarkdownText(value string) string {
	value = strings.Map(func(char rune) rune {
		if char == '\n' || char == '\t' || char < 0x20 || char == 0x7f || char >= 0x80 && char <= 0x9f {
			return ' '
		}
		return char
	}, value)
	return strings.ReplaceAll(value, "`", "'")
}

func (s *Server) handleUsagePrompt(ctx context.Context, incoming message, current *session, lifecycle promptLifecycle, command billing.Command) {
	message := command.Message
	switch command.Action {
	case billing.ShowUsage:
		if current.runner.FetchUsage == nil {
			message = "Usage could not be loaded: billing usage is unavailable"
		} else if text, err := current.runner.FetchUsage(ctx); err != nil {
			message = "Usage could not be loaded: " + err.Error()
		} else {
			message = text
		}
	case billing.ManageUsage:
		message = billing.ManageURL
		if current.runner.OpenURL != nil && current.runner.OpenURL(billing.ManageURL) {
			message = "Opened usage management: " + billing.ManageURL
		}
	}
	s.handleLocalMessagePrompt(incoming, current, lifecycle, message)
}

func (s *Server) handleSharePrompt(ctx context.Context, incoming message, current *session, lifecycle promptLifecycle) {
	message := "Sharing is disabled"
	if current.runner.SharingEnabled != nil && current.runner.SharingEnabled() {
		if current.runner.ShareSession == nil || strings.TrimSpace(current.runner.SessionID) == "" {
			message = "No active session to share"
		} else if url, err := current.runner.ShareSession(ctx); err != nil {
			message = "Couldn't share session: " + err.Error()
		} else {
			message = "Session shared: " + url
		}
	}
	s.handleLocalMessagePrompt(incoming, current, lifecycle, message)
}

func (s *Server) handleReleaseNotesPrompt(ctx context.Context, incoming message, current *session, lifecycle promptLifecycle) {
	message := changelog.ErrUnavailable.Error()
	if current.runner.FetchReleaseNotes != nil {
		if notes, err := current.runner.FetchReleaseNotes(ctx); err != nil {
			message = err.Error()
		} else {
			message = notes
		}
	}
	s.handleLocalMessagePrompt(incoming, current, lifecycle, message)
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
