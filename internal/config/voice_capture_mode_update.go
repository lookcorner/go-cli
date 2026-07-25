package config

import "strings"

func canonicalVoiceCaptureMode(value string) string {
	if strings.EqualFold(strings.TrimSpace(value), "toggle") {
		return "toggle"
	}
	return "hold"
}

func UpdateVoiceCaptureMode(path, value string) error {
	canonical := canonicalVoiceCaptureMode(value)
	return updateUserConfig(path, func(root map[string]any) error {
		ui, _ := root["ui"].(map[string]any)
		if ui == nil {
			ui = make(map[string]any)
		}
		ui["voice_capture_mode"] = canonical
		root["ui"] = ui
		return nil
	})
}
