package config

import "fmt"

func UpdateHunkTrackerMode(path, mode string) error {
	if mode != "agent_only" && mode != "all_dirty" && mode != "off" {
		return fmt.Errorf("unknown hunk tracker mode %q", mode)
	}
	return updateUserConfig(path, func(root map[string]any) error {
		ui, _ := root["ui"].(map[string]any)
		if ui == nil {
			ui = make(map[string]any)
		}
		ui["hunk_tracker_mode"] = mode
		root["ui"] = ui
		return nil
	})
}
