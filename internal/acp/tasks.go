package acp

import (
	"context"
	"encoding/json"
	"time"

	"github.com/lookcorner/go-cli/internal/tools"
)

func (s *Server) NotifyTaskBackgrounded(sessionID string, event tools.ProcessBackgrounded) {
	update := map[string]any{
		"sessionUpdate": "task_backgrounded", "tool_call_id": event.ToolCallID,
		"task_id": event.TaskID, "command": event.Command, "cwd": event.CWD,
		"output_file": event.OutputFile,
	}
	if event.Description != "" {
		update["description"] = event.Description
	}
	s.write(map[string]any{
		"jsonrpc": "2.0", "method": "x.ai/task_backgrounded",
		"params": map[string]any{"sessionId": sessionID, "update": update},
	})
}

func (s *Server) NotifyTaskCompleted(sessionID string, snapshot tools.ProcessSnapshot) {
	s.write(map[string]any{
		"jsonrpc": "2.0", "method": "x.ai/task_completed",
		"params": map[string]any{"sessionId": sessionID, "update": map[string]any{
			"sessionUpdate": "task_completed", "task_snapshot": snapshot, "will_wake": false,
		}},
	})
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
	return map[string]any{
		"subagentId": result.ID, "parentSessionId": parentID, "childSessionId": result.ID,
		"subagentType": result.Type, "description": result.Description,
		"startedAtEpochMs": result.StartedAtMS, "durationMs": result.DurationMS,
		"turnCount": result.Turns, "toolCallCount": result.ToolCalls,
		"tokensUsed": 0, "contextWindowTokens": 0, "contextUsagePct": 0,
		"toolsUsed": []string{}, "errorCount": 0,
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
		item["output"], item["toolCalls"], item["turns"] = result.Output, result.ToolCalls, result.Turns
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
