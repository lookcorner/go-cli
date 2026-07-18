package acp

import (
	"context"
	"encoding/json"
)

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
		if current.runner.ListSubagents == nil {
			s.respond(incoming.ID, map[string]any{"result": map[string]any{"tasks": []any{}}, "error": nil})
			return
		}
		results := current.runner.ListSubagents()
		items := make([]map[string]any, 0, len(results))
		for _, result := range results {
			item := map[string]any{
				"taskId": result.ID, "subagentId": result.ID, "subagentType": result.Type,
				"description": result.Description, "status": result.Status,
				"startedAtEpochMs": result.StartedAtMS, "durationMs": result.DurationMS,
				"toolCalls": result.ToolCalls, "turns": result.Turns,
			}
			if result.Output != "" {
				item["output"] = result.Output
			}
			if result.WorktreeDir != "" {
				item["worktreePath"] = result.WorktreeDir
			}
			items = append(items, item)
		}
		s.respond(incoming.ID, map[string]any{"result": map[string]any{"tasks": items}, "error": nil})
		return
	}
	id := firstString(req.TaskID, req.TaskID2)
	if id == "" || current.runner.KillSubagent == nil {
		s.respondError(incoming.ID, -32602, "taskId is required")
		return
	}
	outcome, err := current.runner.KillSubagent(ctx, id)
	if err != nil {
		s.respond(incoming.ID, map[string]any{"result": nil, "error": err.Error()})
		return
	}
	s.respond(incoming.ID, map[string]any{"result": map[string]any{"taskId": id, "outcome": outcome}, "error": nil})
}
