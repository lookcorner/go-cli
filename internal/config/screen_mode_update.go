package config

import "errors"

func UpdateScreenMode(path, mode string) error {
	if mode != "fullscreen" && mode != "minimal" {
		return errors.New("screen mode must be fullscreen or minimal")
	}
	return updateUserConfig(path, func(root map[string]any) error {
		ui, _ := root["ui"].(map[string]any)
		if ui == nil {
			ui = make(map[string]any)
		}
		ui["screen_mode"] = mode
		root["ui"] = ui
		return nil
	})
}
