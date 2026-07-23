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
	return updateUserConfig(path, func(root map[string]any) error {
		ui, _ := root["ui"].(map[string]any)
		if ui == nil {
			ui = make(map[string]any)
		}
		ui["theme"] = canonical
		root["ui"] = ui
		return nil
	})
}
