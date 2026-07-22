package acp

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/lookcorner/go-cli/internal/agent"
	sessionlog "github.com/lookcorner/go-cli/internal/session"
)

type sessionModelState struct {
	CurrentModelID string      `json:"currentModelId"`
	Available      []modelInfo `json:"availableModels"`
}

type modelInfo struct {
	ModelID     string         `json:"modelId"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Meta        map[string]any `json:"_meta,omitempty"`
}

func modelState(runner *agent.Runner) sessionModelState {
	if runner == nil {
		return sessionModelState{Available: []modelInfo{}}
	}
	current := runner.ModelID
	if current == "" {
		current = runner.Model
	}
	available := make([]modelInfo, 0, len(runner.ModelOptions))
	for _, option := range runner.ModelOptions {
		if option.Hidden {
			continue
		}
		meta := map[string]any{}
		if option.ContextWindow > 0 {
			meta["totalContextTokens"] = option.ContextWindow
		}
		if option.SupportsReasoningEffort {
			meta["supportsReasoningEffort"] = true
			effort := option.ReasoningEffort
			if option.ID == current && strings.TrimSpace(runner.ReasoningEffort) != "" {
				effort = runner.ReasoningEffort
			}
			if effort != "" {
				meta["reasoningEffort"] = effort
			}
		}
		if len(option.ReasoningEfforts) > 0 {
			efforts := make([]map[string]any, 0, len(option.ReasoningEfforts))
			for _, effort := range option.ReasoningEfforts {
				item := map[string]any{"id": effort.ID, "value": effort.Value, "label": effort.Label, "default": effort.Default}
				if effort.Description != "" {
					item["description"] = effort.Description
				}
				efforts = append(efforts, item)
			}
			meta["reasoningEfforts"] = efforts
		}
		if len(meta) == 0 {
			meta = nil
		}
		available = append(available, modelInfo{ModelID: option.ID, Name: option.Name, Description: option.Description, Meta: meta})
	}
	if len(runner.ModelOptions) == 0 && current != "" {
		available = append(available, modelInfo{ModelID: current, Name: current})
	}
	return sessionModelState{CurrentModelID: current, Available: available}
}

func (s *Server) handleSetSessionModel(incoming message) {
	var params struct {
		SessionID string         `json:"sessionId"`
		ModelID   string         `json:"modelId"`
		Meta      map[string]any `json:"_meta"`
	}
	if json.Unmarshal(incoming.Params, &params) != nil || params.SessionID == "" || params.ModelID == "" {
		s.respondError(incoming.ID, -32602, "sessionId and modelId are required")
		return
	}
	current := s.lookupSession(params.SessionID)
	if current == nil {
		s.respondError(incoming.ID, -32602, "unknown session")
		return
	}
	current.mu.Lock()
	defer current.mu.Unlock()
	if current.closed || current.runner == nil {
		s.respondError(incoming.ID, -32602, "unknown session")
		return
	}
	if current.running || current.startingPromptID != "" || current.btwDone != nil || current.recapDone != nil || current.suggestDone != nil {
		s.respondError(incoming.ID, -32000, "cannot change model while the session is busy")
		return
	}
	selectable := false
	for _, option := range current.runner.ModelOptions {
		if option.ID == params.ModelID {
			selectable = true
			break
		}
	}
	if !selectable {
		s.respondError(incoming.ID, -32602, "unknown model id")
		return
	}
	messages, err := sessionlog.TranscriptOrEmpty(current.logPath)
	if err != nil {
		s.respondError(incoming.ID, -32000, err.Error())
		return
	}
	if current.runner.ResolveModel == nil {
		s.respondError(incoming.ID, -32000, "session model switching is unavailable")
		return
	}
	runtime, err := current.runner.ResolveModel(params.ModelID)
	if err != nil {
		s.respondError(incoming.ID, -32000, err.Error())
		return
	}
	if runtime.Client == nil || strings.TrimSpace(runtime.ID) == "" || strings.TrimSpace(runtime.Model) == "" {
		s.respondError(incoming.ID, -32000, "resolved model runtime is incomplete")
		return
	}
	if runtime.SupportsReasoningEffort {
		if effort := reasoningEffortOverride(params.Meta); effort != "" {
			runtime.ReasoningEffort = effort
		}
	} else {
		runtime.ReasoningEffort = ""
	}
	if current.runner.Logger != nil {
		if err := current.runner.Logger.Append("session_model", map[string]any{"model_id": runtime.ID, "reasoning_effort": runtime.ReasoningEffort}); err != nil {
			s.respondError(incoming.ID, -32000, err.Error())
			return
		}
	}
	if err := current.runner.ApplyModel(runtime, messages); err != nil {
		s.respondError(incoming.ID, -32000, err.Error())
		return
	}
	current.previous = ""
	current.updated = time.Now().UTC()
	update := map[string]any{"sessionUpdate": "model_changed", "model_id": runtime.ID}
	if effort := strings.TrimSpace(current.runner.ReasoningEffort); effort != "" {
		update["reasoning_effort"] = effort
	}
	s.write(map[string]any{
		"jsonrpc": "2.0", "method": "x.ai/session_notification",
		"params": map[string]any{"sessionId": current.id, "update": update},
	})
	s.respond(incoming.ID, map[string]any{"_meta": map[string]any{"model": runtime.Model}})
}

func reasoningEffortOverride(meta map[string]any) string {
	value, _ := meta["reasoningEffort"].(string)
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "none", "minimal", "low", "medium", "high", "xhigh":
		return strings.ToLower(strings.TrimSpace(value))
	case "max":
		return "xhigh"
	default:
		return ""
	}
}
