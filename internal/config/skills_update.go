package config

import (
	"errors"
	"fmt"
)

func UpdateSkills(path string, update func(*SkillsConfig)) error {
	if update == nil {
		return errors.New("skills update is required")
	}
	return updateUserConfig(path, func(root map[string]any) error {
		settings, err := readConfigSection[SkillsConfig](root, "skills")
		if err != nil {
			return fmt.Errorf("parse skills config: %w", err)
		}
		update(&settings)
		if len(settings.Paths) == 0 && len(settings.Ignore) == 0 && len(settings.Disabled) == 0 {
			delete(root, "skills")
		} else {
			root["skills"] = map[string]any{
				"paths": settings.Paths, "ignore": settings.Ignore, "disabled": settings.Disabled,
			}
		}
		return nil
	})
}
