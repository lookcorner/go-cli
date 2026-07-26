package config

func UpdateContextualUndoHint(path string, enabled bool) error {
	return updateContextualHint(path, "undo", enabled)
}

func UpdateContextualPlanModeHint(path string, enabled bool) error {
	return updateContextualHint(path, "plan_mode", enabled)
}

func UpdateContextualImageInputHint(path string, enabled bool) error {
	return updateContextualHint(path, "image_input", enabled)
}

func UpdateContextualSendNowHint(path string, enabled bool) error {
	return updateContextualHint(path, "send_now", enabled)
}

func UpdateContextualSmallScreenHint(path string, enabled bool) error {
	return updateContextualHint(path, "small_screen", enabled)
}

func UpdateContextualWordSelectHint(path string, enabled bool) error {
	return updateContextualHint(path, "word_select", enabled)
}

func UpdateContextualSSHWrapHint(path string, enabled bool) error {
	return updateContextualHint(path, "ssh_wrap", enabled)
}

func updateContextualHint(path, name string, enabled bool) error {
	return updateUserConfig(path, func(root map[string]any) error {
		ui, _ := root["ui"].(map[string]any)
		if ui == nil {
			ui = make(map[string]any)
		}
		hints, _ := ui["contextual_hints"].(map[string]any)
		if hints == nil {
			hints = make(map[string]any)
		}
		hints[name] = enabled
		ui["contextual_hints"] = hints
		root["ui"] = ui
		return nil
	})
}
