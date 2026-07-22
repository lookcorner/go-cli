package acp

import (
	"encoding/json"
	"errors"
	"fmt"
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
		if option.Hidden || option.Disallowed {
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
			if option.Disallowed {
				s.respondError(incoming.ID, -32602, "model is not allowed by allowed_models")
				return
			}
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
	current.unavailableModel = ""
	current.pendingModelID = ""
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

// ReloadModels refreshes every live session catalog and emits the reference
// machine-wide notification when a model-related config change is detected.
func (s *Server) ReloadModels() error {
	_, err := s.reloadModels()
	return err
}

func (s *Server) handleModelReload(incoming message) {
	count, err := s.reloadModels()
	if err != nil {
		s.respondError(incoming.ID, -32000, err.Error())
		return
	}
	s.respond(incoming.ID, map[string]any{"models": count})
}

func (s *Server) reloadModels() (int, error) {
	s.mu.Lock()
	sessions := make([]*session, 0, len(s.sessions))
	for _, current := range s.sessions {
		sessions = append(sessions, current)
	}
	s.mu.Unlock()
	modelCount := 0
	var reloadErr error
	for _, current := range sessions {
		if current == nil {
			continue
		}
		if err := s.reloadSessionModels(current); err != nil {
			reloadErr = errors.Join(reloadErr, fmt.Errorf("session %s: %w", current.id, err))
		}
		current.mu.Lock()
		if !current.closed && current.runner != nil && current.runner.ReloadModels != nil {
			modelCount = max(modelCount, len(current.runner.ModelOptions))
		}
		current.mu.Unlock()
	}
	return modelCount, reloadErr
}

func (s *Server) reloadSessionModels(current *session) error {
	current.mu.Lock()
	if current.closed || current.runner == nil || current.runner.ReloadModels == nil {
		current.mu.Unlock()
		return nil
	}
	update, err := current.runner.ReloadModels()
	if err != nil || !update.Changed {
		current.mu.Unlock()
		return err
	}
	current.runner.ModelOptions = append([]agent.ModelOption(nil), update.Options...)
	previous := current.runner.ModelID
	if previous == "" {
		previous = current.runner.Model
	}
	currentAvailable := modelOptionAvailable(update.Options, previous, false)
	target := ""
	if update.PreferredChanged && modelOptionAvailable(update.Options, update.PreferredID, true) {
		target = update.PreferredID
	} else if !currentAvailable {
		target = firstAvailableModel(update.Options, update.PreferredID)
	}
	busy := current.running || current.startingPromptID != "" || current.btwDone != nil || current.recapDone != nil || current.suggestDone != nil
	switched := ""
	if target != "" && target != previous {
		if busy {
			current.pendingModelID = target
			if !currentAvailable {
				current.unavailableModel = previous
			}
		} else if err := switchSessionModelLocked(current, target); err != nil {
			current.mu.Unlock()
			return err
		} else {
			switched = target
		}
	} else if !currentAvailable {
		current.unavailableModel = previous
		current.pendingModelID = ""
	} else {
		current.unavailableModel = ""
		current.pendingModelID = ""
	}
	state := modelState(current.runner)
	current.mu.Unlock()
	s.write(map[string]any{"jsonrpc": "2.0", "method": "x.ai/models/update", "params": state})
	if switched != "" {
		s.notifyModelAutoSwitched(current.id, previous, switched, fmt.Sprintf("Model catalog changed; using %q.", switched))
	}
	return nil
}

func modelOptionAvailable(options []agent.ModelOption, id string, visibleOnly bool) bool {
	for _, option := range options {
		if (option.ID == id || option.Model == id) && !option.Disallowed && (!visibleOnly || !option.Hidden) {
			return true
		}
	}
	return false
}

func firstAvailableModel(options []agent.ModelOption, preferred string) string {
	if modelOptionAvailable(options, preferred, true) {
		return preferred
	}
	for _, option := range options {
		if !option.Hidden && !option.Disallowed {
			return option.ID
		}
	}
	for _, option := range options {
		if !option.Disallowed {
			return option.ID
		}
	}
	return ""
}

func switchSessionModelLocked(current *session, id string) error {
	if current.runner.ResolveModel == nil {
		return errors.New("session model switching is unavailable")
	}
	messages, err := sessionlog.TranscriptOrEmpty(current.logPath)
	if err != nil {
		return err
	}
	runtime, err := current.runner.ResolveModel(id)
	if err != nil {
		return err
	}
	if runtime.Client == nil || strings.TrimSpace(runtime.ID) == "" || strings.TrimSpace(runtime.Model) == "" {
		return errors.New("resolved model runtime is incomplete")
	}
	if current.runner.Logger != nil {
		if err := current.runner.Logger.Append("session_model", map[string]any{"model_id": runtime.ID, "reasoning_effort": runtime.ReasoningEffort}); err != nil {
			return err
		}
	}
	if err := current.runner.ApplyModel(runtime, messages); err != nil {
		return err
	}
	current.previous = ""
	current.unavailableModel = ""
	current.pendingModelID = ""
	current.updated = time.Now().UTC()
	return nil
}

func (s *Server) applyPendingModel(current *session) error {
	current.mu.Lock()
	previous, target := current.runner.ModelID, current.pendingModelID
	if target == "" {
		current.mu.Unlock()
		return nil
	}
	if err := switchSessionModelLocked(current, target); err != nil {
		current.mu.Unlock()
		return err
	}
	current.mu.Unlock()
	s.notifyModelAutoSwitched(current.id, previous, target, fmt.Sprintf("Model catalog changed; using %q.", target))
	return nil
}

func modelRequestAvailable(runner *agent.Runner, requested string, visibleOnly bool) bool {
	if runner == nil {
		return false
	}
	if len(runner.ModelOptions) == 0 {
		return true
	}
	for _, option := range runner.ModelOptions {
		if (option.ID == requested || option.Model == requested) && !option.Disallowed && (!visibleOnly || !option.Hidden) {
			return true
		}
	}
	return false
}

func hasAllowedModel(runner *agent.Runner) bool {
	for _, option := range runner.ModelOptions {
		if !option.Disallowed {
			return true
		}
	}
	return len(runner.ModelOptions) == 0
}

func sameModelFamily(first, second string) bool {
	return strings.HasPrefix(first, "grok-build") == strings.HasPrefix(second, "grok-build")
}

func (s *Server) notifyModelAutoSwitched(sessionID, previous, next, reason string) {
	s.write(map[string]any{
		"jsonrpc": "2.0", "method": "x.ai/session_notification",
		"params": map[string]any{"sessionId": sessionID, "update": map[string]any{
			"sessionUpdate": "model_auto_switched", "previous_model_id": previous, "new_model_id": next, "reason": reason,
		}},
	})
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
