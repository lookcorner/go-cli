package config

import "github.com/lookcorner/go-cli/internal/voice"

func UpdateVoiceSTTLanguage(path, value string) error {
	canonical := voice.CanonicalLanguage(value)
	return updateUserConfig(path, func(root map[string]any) error {
		ui, _ := root["ui"].(map[string]any)
		if ui == nil {
			ui = make(map[string]any)
		}
		ui["voice_stt_language"] = canonical
		root["ui"] = ui
		return nil
	})
}
