package config

import (
	"strings"
)

func UpdateDefaultModel(path, id string) error {
	id = strings.TrimSpace(id)
	return updateUserConfig(path, func(root map[string]any) error {
		models, _ := root["models"].(map[string]any)
		if models == nil {
			models = make(map[string]any)
		}
		if id == "" {
			delete(models, "default")
		} else {
			models["default"] = id
		}
		root["models"] = models
		return nil
	})
}
