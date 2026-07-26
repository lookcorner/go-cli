package config

func UpdateShowTips(path string, enabled bool) error {
	return updateUserConfig(path, func(root map[string]any) error {
		cli, _ := root["cli"].(map[string]any)
		if cli == nil {
			cli = make(map[string]any)
		}
		cli["show_tips"] = enabled
		root["cli"] = cli
		return nil
	})
}
