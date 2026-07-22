package acp

import (
	"strings"

	"github.com/lookcorner/go-cli/internal/agent"
)

type sessionConfigOption struct {
	ID          string `json:"id"`
	Category    string `json:"category"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	Selected    bool   `json:"selected"`
}

type sessionDetail struct {
	SessionID      string `json:"sessionId"`
	Kind           string `json:"kind"`
	CWD            string `json:"cwd"`
	CurrentModelID string `json:"currentModelId"`
	Title          string `json:"title,omitempty"`
}

func sessionStartResponse(current *session, mode string) map[string]any {
	current.mu.Lock()
	id, cwd, title, runner := current.id, current.cwd, current.title, current.runner
	state := modelState(runner)
	options := sessionConfigOptions(runner, state)
	current.mu.Unlock()
	return map[string]any{
		"sessionId": id, "modes": sessionModes(mode), "models": state,
		"_meta": map[string]any{
			"x.ai/sessionConfig": map[string]any{"options": options},
			"x.ai/sessionDetail": sessionDetail{
				SessionID: id, Kind: "build", CWD: cwd, CurrentModelID: state.CurrentModelID, Title: title,
			},
		},
	}
}

func sessionConfigOptions(runner *agent.Runner, state sessionModelState) []sessionConfigOption {
	options := make([]sessionConfigOption, 0, len(state.Available)+5)
	for _, model := range state.Available {
		label := strings.TrimSpace(model.Name)
		if label == "" {
			label = model.ModelID
		}
		options = append(options, sessionConfigOption{
			ID: model.ModelID, Category: "model", Label: label, Selected: model.ModelID == state.CurrentModelID,
		})
	}
	if runner == nil {
		return options
	}
	var current *agent.ModelOption
	for index := range runner.ModelOptions {
		option := &runner.ModelOptions[index]
		if option.ID == state.CurrentModelID || option.Model == state.CurrentModelID {
			current = option
			break
		}
	}
	if current == nil || !current.SupportsReasoningEffort {
		return options
	}
	efforts := current.ReasoningEfforts
	if len(efforts) == 0 {
		efforts = []agent.ReasoningEffortOption{
			{ID: "minimal", Value: "minimal", Label: "Minimal"},
			{ID: "low", Value: "low", Label: "Low"},
			{ID: "medium", Value: "medium", Label: "Medium"},
			{ID: "high", Value: "high", Label: "High"},
			{ID: "xhigh", Value: "xhigh", Label: "X-High"},
		}
	}
	selected := strings.TrimSpace(runner.ReasoningEffort)
	if selected == "" {
		selected = current.ReasoningEffort
	}
	if selected == "" {
		for _, effort := range efforts {
			if effort.Default {
				selected = effort.Value
				break
			}
		}
	}
	for _, effort := range efforts {
		options = append(options, sessionConfigOption{
			ID: effort.ID, Category: "mode", Label: effort.Label, Description: effort.Description,
			Selected: effort.Value == selected,
		})
	}
	return options
}
