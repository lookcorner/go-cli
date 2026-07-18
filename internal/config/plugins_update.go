package config

import (
	"errors"
	"fmt"
)

func UpdatePlugins(path string, update func(*PluginsConfig)) error {
	if update == nil {
		return errors.New("plugins update is required")
	}
	return updateUserConfig(path, func(root map[string]any) error {
		settings, err := readConfigSection[PluginsConfig](root, "plugins")
		if err != nil {
			return fmt.Errorf("parse plugins config: %w", err)
		}
		update(&settings)
		if len(settings.Paths) == 0 && len(settings.Enabled) == 0 && len(settings.Disabled) == 0 {
			delete(root, "plugins")
		} else {
			root["plugins"] = map[string]any{
				"paths": settings.Paths, "enabled": settings.Enabled, "disabled": settings.Disabled,
			}
		}
		return nil
	})
}
