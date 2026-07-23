package config

func UpdateShowTimestamps(path string, enabled bool) error {
	return updateUserConfig(path, func(root map[string]any) error {
		ui, _ := root["ui"].(map[string]any)
		if ui == nil {
			ui = make(map[string]any)
		}
		ui["show_timestamps"] = enabled
		root["ui"] = ui
		return nil
	})
}
