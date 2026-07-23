package config

func UpdateCompactMode(path string, enabled bool) error {
	return updateUserConfig(path, func(root map[string]any) error {
		ui, _ := root["ui"].(map[string]any)
		if ui == nil {
			ui = make(map[string]any)
		}
		ui["compact_mode"] = enabled
		root["ui"] = ui
		return nil
	})
}
