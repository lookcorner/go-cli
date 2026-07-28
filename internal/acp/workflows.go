package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/workflow"
)

func workflowManagementMessage(runner *agent.Runner, prompt string) (string, bool) {
	if runner == nil || runner.Tools == nil || !runner.Tools.HasTool("workflow") {
		return "", false
	}
	fields := strings.Fields(strings.TrimSpace(prompt))
	if len(fields) == 1 && fields[0] == "/workflows" {
		runs := runner.Tools.WorkflowRuns()
		if len(runs) == 0 {
			return "No workflow runs yet.", true
		}
		var b strings.Builder
		b.WriteString(fmt.Sprintf("Workflow runs (%d):\n", len(runs)))
		for _, run := range runs {
			phase := ""
			if run.Phase != "" {
				phase = " - " + run.Phase
			}
			b.WriteString(fmt.Sprintf("- `%s` - **%s** - %s%s\n", run.ID, run.Name, run.Status, phase))
			if run.Error != "" {
				b.WriteString("  Error: " + run.Error + "\n")
			} else if run.Result != "" {
				b.WriteString("  " + run.Result + "\n")
			}
		}
		return strings.TrimSpace(b.String()), true
	}
	if len(fields) == 3 && fields[0] == "/workflow" && strings.EqualFold(fields[2], "stop") {
		if runner.Tools.StopWorkflow(fields[1]) {
			return fmt.Sprintf("Workflow `%s` is stopping.", fields[1]), true
		}
		return fmt.Sprintf("Workflow `%s` is not running.", fields[1]), true
	}
	return "", false
}

func appendWorkflowCommands(commands []map[string]any, runner *agent.Runner, cwd string, workspaceSkills bool) []map[string]any {
	if runner == nil || runner.Tools == nil || !runner.Tools.HasTool("workflow") {
		return commands
	}
	taken := make(map[string]bool, len(commands))
	for _, command := range commands {
		taken[command["name"].(string)] = true
	}
	if runner.Skills != nil {
		for _, item := range runner.Skills.List() {
			if item.Enabled && item.UserInvocable && (workspaceSkills || item.Scope == "user") {
				taken[item.Name] = true
			}
		}
	}
	for _, item := range workflow.List(cwd) {
		if taken[item.Name] {
			continue
		}
		meta := map[string]any{"workflowSource": item.Source}
		if item.Path != nil {
			meta["workflowPath"] = *item.Path
		}
		commands = append(commands, availableCommand(item.Name, "Workflow: "+item.Description, "<args>", meta))
	}
	return commands
}

func resolveWorkflowSlashCommand(runner *agent.Runner, cwd, prompt string) (string, string, bool) {
	trimmed := strings.TrimSpace(prompt)
	fields := strings.Fields(trimmed)
	if len(fields) == 0 || !strings.HasPrefix(fields[0], "/") {
		return "", "", false
	}
	name := strings.TrimPrefix(strings.ToLower(fields[0]), "/")
	for _, command := range availableCommandsForCWD(runner, cwd != "", cwd) {
		if command["name"] != name {
			continue
		}
		meta, _ := command["_meta"].(map[string]any)
		if meta["workflowSource"] == nil {
			return "", "", false
		}
		return name, strings.TrimSpace(strings.TrimPrefix(trimmed, fields[0])), true
	}
	return "", "", false
}

func (s *Server) handleWorkflowSlashPrompt(parent context.Context, incoming message, current *session, lifecycle promptLifecycle, name, input string) {
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
	runCtx, cancel := context.WithCancel(parent)
	runDone := make(chan struct{})
	registry := current.runner.Tools
	current.cancel = cancel
	current.running = true
	current.runDone = runDone
	current.runningPromptID = lifecycle.promptID
	current.updated = time.Now().UTC()
	current.mu.Unlock()

	request := map[string]any{"name": name}
	if args := workflow.ArgumentsFromInput(input); len(args) > 0 {
		request["args"] = args
	}
	arguments, err := json.Marshal(request)
	if err != nil {
		cancel()
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer cancel()
		s.notifyRosterUpsert(current, "working")
		result := ""
		if err == nil {
			result, err = registry.Execute(runCtx, "workflow", arguments)
		}
		stopReason := "end_turn"
		if runCtx.Err() != nil {
			stopReason = "cancelled"
		} else if err == nil {
			s.sendCommandOutput(current.id, result)
		}
		current.mu.Lock()
		current.running = false
		current.runDone = nil
		current.runningPromptID = ""
		current.cancel = nil
		cancelTrigger := current.cancelTrigger
		current.cancelTrigger = ""
		current.updated = time.Now().UTC()
		close(runDone)
		current.mu.Unlock()
		s.notifyRosterUpsert(current, "idle")
		s.finishPrompt(incoming, current, lifecycle, stopReason, agent.Result{}, err, cancelTrigger)
		s.startNext(current)
	}()
}

func (s *Server) handleWorkflowsList(incoming message) {
	var req struct {
		SessionID string `json:"sessionId"`
	}
	if json.Unmarshal(incoming.Params, &req) != nil || req.SessionID == "" {
		s.respondError(incoming.ID, -32602, "sessionId is required")
		return
	}
	current := s.lookupSession(req.SessionID)
	if current == nil {
		s.respond(incoming.ID, map[string]any{
			"result": nil,
			"error":  "unknown session id: " + req.SessionID,
		})
		return
	}
	current.mu.Lock()
	cwd := current.cwd
	closed := current.closed
	current.mu.Unlock()
	if closed {
		s.respond(incoming.ID, map[string]any{
			"result": nil,
			"error":  "unknown session id: " + req.SessionID,
		})
		return
	}
	// Catalog discovery is independent from execution through the workflow tool.
	// Always list when the session exists (Rust gates on launches).
	listings := workflow.List(cwd)
	items := make([]map[string]any, 0, len(listings))
	for _, item := range listings {
		row := map[string]any{
			"name":        item.Name,
			"description": item.Description,
			"source":      item.Source,
		}
		if item.WhenToUse != nil {
			row["when_to_use"] = *item.WhenToUse
		}
		if item.Path != nil {
			row["path"] = *item.Path
		}
		items = append(items, row)
	}
	s.respond(incoming.ID, map[string]any{"result": map[string]any{"workflows": items}})
}
