package config

func UpdateContextualUndoHint(path string, enabled bool) error {
	return updateUserConfig(path, func(root map[string]any) error {
		ui, _ := root["ui"].(map[string]any)
		if ui == nil {
			ui = make(map[string]any)
		}
		hints, _ := ui["contextual_hints"].(map[string]any)
		if hints == nil {
			hints = make(map[string]any)
		}
		hints["undo"] = enabled
		ui["contextual_hints"] = hints
		root["ui"] = ui
		return nil
	})
}
