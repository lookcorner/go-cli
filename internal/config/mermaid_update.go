package config

import "errors"

func UpdateRenderMermaid(path, mode string) error {
	if mode != "auto" && mode != "on" && mode != "off" {
		return errors.New("Mermaid rendering must be auto, on, or off")
	}
	return updateUserConfig(path, func(root map[string]any) error {
		ui, _ := root["ui"].(map[string]any)
		if ui == nil {
			ui = make(map[string]any)
		}
		ui["render_mermaid"] = mode
		root["ui"] = ui
		return nil
	})
}
