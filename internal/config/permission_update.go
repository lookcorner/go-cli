package config

func UpdatePermissionMode(path, mode string) error {
	mode, err := normalizePermissionMode(mode)
	if err != nil {
		return err
	}
	return updateUserConfig(path, func(root map[string]any) error {
		ui, _ := root["ui"].(map[string]any)
		if ui == nil {
			ui = make(map[string]any)
		}
		ui["permission_mode"] = mode
		root["ui"] = ui
		return nil
	})
}
