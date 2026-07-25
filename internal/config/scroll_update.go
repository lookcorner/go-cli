package config

func UpdateScrollSpeed(path string, value uint8) error {
	value = min(max(value, 1), 100)
	return updateUIValue(path, "scroll_speed", value)
}

func UpdateScrollLines(path string, value uint8) error {
	value = min(max(value, 1), 10)
	return updateUIValue(path, "scroll_lines", value)
}

func updateUIValue(path, key string, value any) error {
	return updateUserConfig(path, func(root map[string]any) error {
		ui, _ := root["ui"].(map[string]any)
		if ui == nil {
			ui = make(map[string]any)
		}
		ui[key] = value
		root["ui"] = ui
		return nil
	})
}
