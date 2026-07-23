package config

import (
	"errors"
	"strings"
)

func UpdateDefaultModel(path, id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("default model id is required")
	}
	return updateUserConfig(path, func(root map[string]any) error {
		models, _ := root["models"].(map[string]any)
		if models == nil {
			models = make(map[string]any)
		}
		models["default"] = id
		root["models"] = models
		return nil
	})
}
