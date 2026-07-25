package config

import "fmt"

func UpdateScrollMode(path, mode string) error {
	if mode != "auto" && mode != "wheel" && mode != "trackpad" {
		return fmt.Errorf("unknown scroll mode %q", mode)
	}
	return updateUserConfig(path, func(root map[string]any) error {
		ui, _ := root["ui"].(map[string]any)
		if ui == nil {
			ui = make(map[string]any)
		}
		ui["scroll_mode"] = mode
		root["ui"] = ui
		return nil
	})
}
