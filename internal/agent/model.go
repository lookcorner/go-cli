package agent

import (
	"errors"
	"fmt"
	"strings"

	"github.com/lookcorner/go-cli/internal/session"
)

func (r *Runner) AvailableModels() []ModelOption {
	if r == nil {
		return nil
	}
	models := make([]ModelOption, 0, len(r.ModelOptions))
	for _, option := range r.ModelOptions {
		if option.Hidden || option.Disallowed {
			continue
		}
		option.ReasoningEfforts = append([]ReasoningEffortOption(nil), option.ReasoningEfforts...)
		models = append(models, option)
	}
	return models
}

func (r *Runner) CurrentModel() (ModelOption, bool) {
	if r == nil {
		return ModelOption{}, false
	}
	return r.resolveModelOption(r.ModelID, false)
}

func (r *Runner) SwitchModel(query, effort string) (ModelOption, error) {
	if r == nil || r.ResolveModel == nil {
		return ModelOption{}, errors.New("model switching is unavailable")
	}
	if r.recapRunning.Load() || r.btwRunning.Load() || r.rewind.enabled.Load() && r.rewind.active.Load() >= 0 {
		return ModelOption{}, errors.New("cannot switch models while a model request is running")
	}
	option, ok := r.resolveModelOption(query, !strings.EqualFold(strings.TrimSpace(query), r.ModelID))
	if !ok {
		return ModelOption{}, fmt.Errorf("unknown or unavailable model %q", strings.TrimSpace(query))
	}
	resolvedEffort, err := resolveReasoningEffort(option, effort)
	if err != nil {
		return ModelOption{}, err
	}
	runtime, err := r.ResolveModel(option.ID)
	if err != nil {
		return ModelOption{}, err
	}
	if runtime.Client == nil || strings.TrimSpace(runtime.ID) == "" || strings.TrimSpace(runtime.Model) == "" {
		return ModelOption{}, errors.New("resolved model runtime is incomplete")
	}
	if strings.TrimSpace(effort) != "" {
		runtime.ReasoningEffort = resolvedEffort
	}
	messages := []session.Message{}
	if r.SessionPath != "" {
		messages, err = session.TranscriptOrEmpty(r.SessionPath)
		if err != nil {
			return ModelOption{}, err
		}
	}
	if r.Logger != nil {
		if err := r.Logger.Append("session_model", map[string]any{"model_id": runtime.ID, "reasoning_effort": runtime.ReasoningEffort}); err != nil {
			return ModelOption{}, err
		}
	}
	if err := r.ApplyModel(runtime, messages); err != nil {
		return ModelOption{}, err
	}
	option.ReasoningEffort = runtime.ReasoningEffort
	return option, nil
}

func (r *Runner) SwitchModelCommand(arguments string) (ModelOption, error) {
	arguments = strings.TrimSpace(arguments)
	if arguments == "" {
		return ModelOption{}, errors.New("usage: /model <name> [effort]")
	}
	if option, ok := r.resolveModelOption(arguments, true); ok {
		selected, err := r.SwitchModel(option.ID, "")
		if err == nil && r.SetDefaultModel != nil {
			err = r.SetDefaultModel(option.ID)
			if err != nil {
				err = fmt.Errorf("persist default model: %w", err)
			}
		}
		return selected, err
	}
	for index := len(arguments) - 1; index >= 0; index-- {
		if arguments[index] != ' ' && arguments[index] != '\t' {
			continue
		}
		name, effort := strings.TrimSpace(arguments[:index]), strings.TrimSpace(arguments[index+1:])
		if name == "" || effort == "" {
			continue
		}
		if option, ok := r.resolveModelOption(name, true); ok {
			return r.SwitchModel(option.ID, effort)
		}
		break
	}
	return ModelOption{}, fmt.Errorf("unknown or unavailable model %q", arguments)
}

func (r *Runner) CurrentReasoningEfforts() []ReasoningEffortOption {
	if r == nil {
		return nil
	}
	option, ok := r.resolveModelOption(r.ModelID, false)
	if !ok || !option.SupportsReasoningEffort {
		return nil
	}
	return ReasoningEfforts(option)
}

func (r *Runner) resolveModelOption(query string, visibleOnly bool) (ModelOption, bool) {
	query = strings.TrimSpace(query)
	for _, option := range r.ModelOptions {
		if option.Disallowed || visibleOnly && option.Hidden {
			continue
		}
		if strings.EqualFold(option.ID, query) || strings.EqualFold(option.Model, query) || strings.EqualFold(option.Name, query) {
			return option, true
		}
	}
	return ModelOption{}, false
}

func resolveReasoningEffort(option ModelOption, requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return option.ReasoningEffort, nil
	}
	if !option.SupportsReasoningEffort {
		return "", fmt.Errorf("model %q does not support reasoning effort", option.Name)
	}
	efforts := ReasoningEfforts(option)
	for _, effort := range efforts {
		value := effort.Value
		if value == "" {
			value = effort.ID
		}
		if strings.EqualFold(requested, effort.ID) || strings.EqualFold(requested, value) {
			return value, nil
		}
	}
	available := make([]string, 0, len(efforts))
	for _, effort := range efforts {
		available = append(available, effort.ID)
	}
	return "", fmt.Errorf("unknown effort level %q; use one of: %s", requested, strings.Join(available, ", "))
}

func ReasoningEfforts(option ModelOption) []ReasoningEffortOption {
	if len(option.ReasoningEfforts) > 0 {
		return append([]ReasoningEffortOption(nil), option.ReasoningEfforts...)
	}
	return []ReasoningEffortOption{
		{ID: "xhigh", Value: "xhigh", Label: "Extra high"},
		{ID: "high", Value: "high", Label: "High"},
		{ID: "medium", Value: "medium", Label: "Medium"},
		{ID: "low", Value: "low", Label: "Low"},
	}
}
