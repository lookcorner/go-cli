package config

import (
	"fmt"
	"strings"
)

const maxModelIDBytes = 256

func UpdateForkSecondaryModel(path, id string) error {
	id = strings.TrimSpace(id)
	if len(id) > maxModelIDBytes {
		return fmt.Errorf("fork secondary model id is too long (%d > %d bytes)", len(id), maxModelIDBytes)
	}
	return updateUserConfig(path, func(root map[string]any) error {
		ui, _ := root["ui"].(map[string]any)
		if ui == nil {
			ui = make(map[string]any)
		}
		if id == "" {
			delete(ui, "fork_secondary_model")
		} else {
			ui["fork_secondary_model"] = id
		}
		root["ui"] = ui
		return nil
	})
}
