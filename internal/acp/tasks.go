package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	sessionlog "github.com/lookcorner/go-cli/internal/session"
	"github.com/lookcorner/go-cli/internal/subagent"
	"github.com/lookcorner/go-cli/internal/tools"
)

func (s *Server) NotifySubagentStarted(sessionID string, event subagent.Started) {
	s.notifySubagent(sessionID, SubagentStartedUpdate(sessionID, event))
}

func SubagentStartedUpdate(sessionID string, event subagent.Started) map[string]any {
	update := map[string]any{
		"sessionUpdate": "subagent_spawned", "subagent_id": event.ID,
		"parent_session_id": sessionID, "child_session_id": event.ID,
		"subagent_type": event.Type, "description": event.Description,
		"effective_context_source": "new", "context_normalized": false,
	}
	if event.Model != "" {
		update["model"] = event.Model
	}
	if event.CapabilityMode != "" {
		update["capability_mode"] = event.CapabilityMode
	}
	if event.ResumedFrom != "" {
		update["resumed_from"] = event.ResumedFrom
		update["effective_context_source"] = "resumed"
	}
	return update
}

func (s *Server) NotifySubagentProgress(sessionID string, result tools.SubagentResult) {
	toolsUsed := result.ToolsUsed
	if toolsUsed == nil {
		toolsUsed = []string{}
	}
	s.notifySubagent(sessionID, map[string]any{
		"sessionUpdate": "subagent_progress", "subagent_id": result.ID,
		"parent_session_id": sessionID, "child_session_id": result.ID,
		"duration_ms": result.DurationMS, "turn_count": result.Turns,
		"tool_call_count": result.ToolCalls, "tokens_used": result.TokensUsed,
		"context_window_tokens": result.ContextWindow, "context_usage_pct": result.ContextUsage,
		"tools_used": toolsUsed, "error_count": result.ErrorCount,
	})
}

func (s *Server) NotifySubagentEnded(sessionID string, result tools.SubagentResult) {
	s.notifySubagent(sessionID, SubagentFinishedUpdate(result))
	if result.WillWake {
		if current := s.lookupSession(sessionID); current != nil {
			s.startNextWake(current)
		}
	}
}

func (s *Server) QueueSubagentWake(sessionID string, result tools.SubagentResult) bool {
	return s.queueWake(sessionID, syntheticWake{id: result.ID, prompt: formatSubagentWake(result)})
}

func (s *Server) QueueTaskWake(sessionID string, snapshot tools.ProcessSnapshot) bool {
	return s.queueWake(sessionID, syntheticWake{id: snapshot.TaskID, prompt: formatTaskWake(snapshot)})
}

func (s *Server) queueWake(sessionID string, wake syntheticWake) bool {
	if s.closing.Load() {
		return false
	}
	current := s.lookupSession(sessionID)
	if current == nil {
		return false
	}
	current.mu.Lock()
	if current.closed || current.ctx == nil || current.ctx.Err() != nil || s.closing.Load() {
		current.mu.Unlock()
		return false
	}
	current.wakeQueue = append(current.wakeQueue, wake)
	current.mu.Unlock()
	return true
}

func (s *Server) CancelSubagentWake(sessionID, subagentID string) {
	s.cancelWake(sessionID, subagentID)
}

func (s *Server) CancelTaskWake(sessionID, taskID string) {
	s.cancelWake(sessionID, taskID)
}

func (s *Server) cancelWake(sessionID, id string) {
	current := s.lookupSession(sessionID)
	if current == nil {
		return
	}
	current.mu.Lock()
	kept := current.wakeQueue[:0]
	for _, wake := range current.wakeQueue {
		if wake.id != id {
			kept = append(kept, wake)
		}
	}
	current.wakeQueue = kept
	current.mu.Unlock()
}

func (s *Server) startNextWake(current *session) {
	current.mu.Lock()
	if current.closed || current.ctx == nil || current.ctx.Err() != nil || current.running || len(current.wakeQueue) == 0 {
		current.mu.Unlock()
		return
	}
	wake := current.wakeQueue[0]
	current.wakeQueue = current.wakeQueue[1:]
	runCtx, cancel := context.WithCancel(current.ctx)
	current.cancel, current.running = cancel, true
	current.activePrompt = current.promptIndex
	current.promptIndex++
	current.updated = time.Now().UTC()
	previous, mode := current.previous, current.mode
	current.mu.Unlock()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		baseInstructions := current.runner.Instructions
		current.runner.Instructions = instructionsForMode(baseInstructions, mode)
		turn, err := current.runner.RunSyntheticTurn(runCtx, wake.prompt, previous)
		current.runner.Instructions = baseInstructions
		points, pointsErr := sessionlog.RewindPoints(current.logPath)
		current.mu.Lock()
		if err == nil {
			current.previous = turn.ResponseID
		}
		current.running, current.activePrompt, current.cancel = false, -1, nil
		if pointsErr == nil {
			current.promptIndex = len(points)
		}
		current.updated = time.Now().UTC()
		current.mu.Unlock()
		s.startNextWake(current)
	}()
}

func formatSubagentWake(result tools.SubagentResult) string {
	status := "successfully"
	if result.Status != "completed" {
		status = "with failure"
	}
	return fmt.Sprintf("<system-reminder>\nBackground subagent %q (%s: %q) completed %s.\nDuration: %.1fs | Tool calls: %d | Turns: %d\nUse get_task_output with task_ids [%q] to retrieve the full result.\n</system-reminder>",
		result.ID, result.Type, result.Description, status, float64(result.DurationMS)/1000, result.ToolCalls, result.Turns, result.ID)
}

func formatTaskWake(snapshot tools.ProcessSnapshot) string {
	status := "successfully"
	if snapshot.ExitCode == nil || *snapshot.ExitCode != 0 {
		status = "with failure"
	}
	return fmt.Sprintf("<system-reminder>\nBackground task %q completed %s.\nCommand: %s\nUse get_task_output with task_ids [%q] to retrieve the full output.\n</system-reminder>",
		snapshot.TaskID, status, snapshot.Command, snapshot.TaskID)
}

func SubagentFinishedUpdate(result tools.SubagentResult) map[string]any {
	update := map[string]any{
		"sessionUpdate": "subagent_finished", "subagent_id": result.ID,
		"child_session_id": result.ID, "status": result.Status,
		"tool_calls": result.ToolCalls, "turns": result.Turns,
		"duration_ms": result.DurationMS, "tokens_used": result.TokensUsed,
		"will_wake": result.WillWake,
	}
	if result.Status == "completed" {
		update["output"] = result.Output
	} else if result.Output != "" {
		update["error"] = result.Output
	}
	return update
}

func (s *Server) notifySubagent(sessionID string, update map[string]any) {
	s.write(map[string]any{
		"jsonrpc": "2.0", "method": "x.ai/session_notification",
		"params": map[string]any{"sessionId": sessionID, "update": update},
	})
}

func (s *Server) NotifyTaskBackgrounded(sessionID string, event tools.ProcessBackgrounded) {
	update := TaskBackgroundedUpdate(event)
	s.write(map[string]any{
		"jsonrpc": "2.0", "method": "x.ai/task_backgrounded",
		"params": map[string]any{"sessionId": sessionID, "update": update},
	})
}

func TaskBackgroundedUpdate(event tools.ProcessBackgrounded) map[string]any {
	update := map[string]any{
		"sessionUpdate": "task_backgrounded", "tool_call_id": event.ToolCallID,
		"task_id": event.TaskID, "command": event.Command, "cwd": event.CWD,
		"output_file": event.OutputFile,
	}
	if event.Description != "" {
		update["description"] = event.Description
	}
	return update
}

func (s *Server) NotifyTaskCompleted(sessionID string, snapshot tools.ProcessSnapshot, willWake bool) {
	s.write(map[string]any{
		"jsonrpc": "2.0", "method": "x.ai/task_completed",
		"params": map[string]any{"sessionId": sessionID, "update": TaskCompletedUpdate(snapshot, willWake)},
	})
	if willWake {
		if current := s.lookupSession(sessionID); current != nil {
			s.startNextWake(current)
		}
	}
}

func TaskCompletedUpdate(snapshot tools.ProcessSnapshot, willWake bool) map[string]any {
	return map[string]any{"sessionUpdate": "task_completed", "task_snapshot": snapshot, "will_wake": willWake}
}

func (s *Server) handleTasks(ctx context.Context, incoming message) {
	var req struct {
		SessionID string `json:"sessionId"`
		TaskID    string `json:"taskId"`
		TaskID2   string `json:"task_id"`
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
	if incoming.Method == "x.ai/task/list" {
		if current.runner.ListTasks == nil {
			s.respond(incoming.ID, map[string]any{"result": map[string]any{"tasks": []any{}}, "error": nil})
			return
		}
		s.respond(incoming.ID, map[string]any{"result": map[string]any{"tasks": current.runner.ListTasks()}, "error": nil})
		return
	}
	id := firstString(req.TaskID, req.TaskID2)
	if id == "" || current.runner.KillTask == nil {
		s.respondError(incoming.ID, -32602, "taskId is required")
		return
	}
	outcome, err := current.runner.KillTask(ctx, id)
	if err != nil {
		s.respond(incoming.ID, map[string]any{"result": nil, "error": err.Error()})
		return
	}
	s.respond(incoming.ID, map[string]any{"result": map[string]any{"taskId": id, "outcome": outcome}, "error": nil})
}

func (s *Server) handleSubagents(ctx context.Context, incoming message) {
	var req struct {
		SessionID  string `json:"sessionId"`
		SubagentID string `json:"subagentId"`
		Block      bool   `json:"block"`
		TimeoutMS  *int64 `json:"timeoutMs"`
	}
	if json.Unmarshal(incoming.Params, &req) != nil {
		s.respondError(incoming.ID, -32602, "invalid subagent request")
		return
	}
	switch incoming.Method {
	case "x.ai/subagent/list_running":
		current := s.lookupSession(req.SessionID)
		if req.SessionID == "" || current == nil || current.runner == nil {
			s.respondError(incoming.ID, -32602, "session not found")
			return
		}
		items := make([]map[string]any, 0)
		if current.runner.ListSubagents != nil {
			for _, result := range current.runner.ListSubagents() {
				if result.Status == "running" {
					items = append(items, liveSubagentDTO(current.id, result))
				}
			}
		}
		s.respond(incoming.ID, map[string]any{"result": map[string]any{"subagents": items}, "error": nil})
	case "x.ai/subagent/get":
		current, _, found := s.findSubagent(req.SubagentID)
		if req.SubagentID == "" {
			s.respondError(incoming.ID, -32602, "subagentId is required")
			return
		}
		if !found || current.runner.GetSubagent == nil {
			s.respond(incoming.ID, map[string]any{"result": map[string]any{"snapshot": nil}, "error": nil})
			return
		}
		timeout := time.Duration(0)
		if req.Block {
			timeout = 30 * time.Second
			if req.TimeoutMS != nil {
				if *req.TimeoutMS < 0 || *req.TimeoutMS > int64(time.Duration(1<<63-1)/time.Millisecond) {
					s.respondError(incoming.ID, -32602, "timeoutMs is out of range")
					return
				}
				timeout = time.Duration(*req.TimeoutMS) * time.Millisecond
			}
		}
		result, err := current.runner.GetSubagent(ctx, req.SubagentID, timeout)
		if err != nil {
			s.respond(incoming.ID, map[string]any{"result": nil, "error": err.Error()})
			return
		}
		s.respond(incoming.ID, map[string]any{"result": map[string]any{"snapshot": subagentDTO(current.id, result)}, "error": nil})
	case "x.ai/subagent/cancel":
		if req.SubagentID == "" {
			s.respondError(incoming.ID, -32602, "subagentId is required")
			return
		}
		current, before, found := s.findSubagent(req.SubagentID)
		if !found || current.runner.KillSubagent == nil {
			s.respond(incoming.ID, map[string]any{"result": map[string]any{
				"subagentId": req.SubagentID, "cancelled": false, "outcome": map[string]any{"kind": "not_found"},
			}, "error": nil})
			return
		}
		outcome, err := current.runner.KillSubagent(ctx, req.SubagentID)
		if err != nil {
			s.respond(incoming.ID, map[string]any{"result": nil, "error": err.Error()})
			return
		}
		result := map[string]any{"subagentId": req.SubagentID, "cancelled": outcome == "killed"}
		if outcome == "already_finished" {
			result["outcome"] = map[string]any{"kind": "already_finished", "status": before.Status}
		} else {
			result["outcome"] = map[string]any{"kind": "cancelled"}
		}
		s.respond(incoming.ID, map[string]any{"result": result, "error": nil})
	}
}

func (s *Server) findSubagent(id string) (*session, tools.SubagentResult, bool) {
	if id == "" {
		return nil, tools.SubagentResult{}, false
	}
	s.mu.Lock()
	sessions := make([]*session, 0, len(s.sessions))
	for _, current := range s.sessions {
		sessions = append(sessions, current)
	}
	s.mu.Unlock()
	for _, current := range sessions {
		if current.runner == nil || current.runner.ListSubagents == nil {
			continue
		}
		for _, result := range current.runner.ListSubagents() {
			if result.ID == id {
				return current, result, true
			}
		}
	}
	return nil, tools.SubagentResult{}, false
}

func liveSubagentDTO(parentID string, result tools.SubagentResult) map[string]any {
	toolsUsed := result.ToolsUsed
	if toolsUsed == nil {
		toolsUsed = []string{}
	}
	return map[string]any{
		"subagentId": result.ID, "parentSessionId": parentID, "childSessionId": result.ID,
		"subagentType": result.Type, "description": result.Description,
		"startedAtEpochMs": result.StartedAtMS, "durationMs": result.DurationMS,
		"turnCount": result.Turns, "toolCallCount": result.ToolCalls,
		"tokensUsed": result.TokensUsed, "contextWindowTokens": result.ContextWindow, "contextUsagePct": result.ContextUsage,
		"toolsUsed": toolsUsed, "errorCount": result.ErrorCount,
	}
}

func subagentDTO(parentID string, result tools.SubagentResult) map[string]any {
	item := map[string]any{
		"subagentId": result.ID, "parentSessionId": parentID, "childSessionId": result.ID,
		"subagentType": result.Type, "description": result.Description,
		"startedAtEpochMs": result.StartedAtMS, "durationMs": result.DurationMS, "status": result.Status,
	}
	switch result.Status {
	case "running":
		for key, value := range liveSubagentDTO(parentID, result) {
			item[key] = value
		}
	case "completed":
		toolsUsed := result.ToolsUsed
		if toolsUsed == nil {
			toolsUsed = []string{}
		}
		item["output"], item["toolCalls"], item["turns"] = result.Output, result.ToolCalls, result.Turns
		item["tokensUsed"], item["contextWindowTokens"], item["contextUsagePct"] = result.TokensUsed, result.ContextWindow, result.ContextUsage
		item["toolsUsed"], item["errorCount"] = toolsUsed, result.ErrorCount
		if result.WorktreeDir != "" {
			item["worktreePath"] = result.WorktreeDir
		}
	case "failed":
		item["failureError"] = result.Output
	case "cancelled":
		if result.Output != "" {
			item["cancelReason"] = result.Output
		}
	}
	return item
}
