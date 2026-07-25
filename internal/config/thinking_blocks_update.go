package config

func UpdateShowThinkingBlocks(path string, enabled bool) error {
	return updateUserConfig(path, func(root map[string]any) error {
		ui, _ := root["ui"].(map[string]any)
		if ui == nil {
			ui = make(map[string]any)
			root["ui"] = ui
		}
		ui["show_thinking_blocks"] = enabled
		return nil
	})
}
