package config

import (
	"fmt"

	"github.com/lookcorner/go-cli/internal/theme"
)

func UpdateTheme(path, value string) error {
	canonical, ok := theme.Canonical(value)
	if !ok {
		return fmt.Errorf("unknown theme %q", value)
	}
	return updateThemeValue(path, "theme", canonical)
}

func UpdateAutoDarkTheme(path, value string) error {
	return updateConcreteTheme(path, "auto_dark_theme", value)
}

func UpdateAutoLightTheme(path, value string) error {
	return updateConcreteTheme(path, "auto_light_theme", value)
}

func updateConcreteTheme(path, key, value string) error {
	canonical, ok := theme.Canonical(value)
	if !ok || canonical == "auto" {
		return fmt.Errorf("unknown concrete theme %q", value)
	}
	return updateThemeValue(path, key, canonical)
}

func updateThemeValue(path, key, value string) error {
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
