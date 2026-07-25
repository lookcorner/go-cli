package config

import "fmt"

func UpdateDefaultSelectedPermission(path, value string) error {
	normalized, ok := parseDefaultSelectedPermission(value)
	if !ok || value != normalized {
		return fmt.Errorf("unknown default selected permission %q", value)
	}
	return updateUserConfig(path, func(root map[string]any) error {
		ui, _ := root["ui"].(map[string]any)
		if ui == nil {
			ui = make(map[string]any)
		}
		ui["default_selected_permission"] = value
		root["ui"] = ui
		return nil
	})
}
