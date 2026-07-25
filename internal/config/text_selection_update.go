package config

import "fmt"

func UpdateTextSelection(path, mode string) error {
	if mode != "flash" && mode != "hold" && mode != "word_select" {
		return fmt.Errorf("unknown text selection mode %q", mode)
	}
	return updateUserConfig(path, func(root map[string]any) error {
		ui, _ := root["ui"].(map[string]any)
		if ui == nil {
			ui = make(map[string]any)
		}
		ui["keep_text_selection"] = mode
		root["ui"] = ui
		return nil
	})
}
