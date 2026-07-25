package config

import "fmt"

func UpdateCancelSubagentsOnTurnCancel(path, value string) error {
	normalized, ok := parseCancelSubagentsOnTurnCancel(value)
	if !ok || value != normalized {
		return fmt.Errorf("unknown cancel subagents policy %q", value)
	}
	return updateUserConfig(path, func(root map[string]any) error {
		ui, _ := root["ui"].(map[string]any)
		if ui == nil {
			ui = make(map[string]any)
		}
		if value == "ask" {
			delete(ui, "cancel_subagents_on_turn_cancel")
		} else {
			ui["cancel_subagents_on_turn_cancel"] = value
		}
		root["ui"] = ui
		return nil
	})
}
