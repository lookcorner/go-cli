package config

import "path/filepath"

func PagerPath() (string, error) {
	home, err := PolicyHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "pager.toml"), nil
}

func LoadRespectManualFolds(path string) (bool, error) {
	root, err := readConfigMap(path)
	if err != nil {
		return false, err
	}
	scrollback, _ := root["scrollback"].(map[string]any)
	scroll, _ := scrollback["scroll"].(map[string]any)
	enabled, _ := scroll["respect_manual_folds"].(bool)
	return enabled, nil
}

func UpdateRespectManualFolds(path string, enabled bool) error {
	return updateUserConfig(path, func(root map[string]any) error {
		scrollback, _ := root["scrollback"].(map[string]any)
		if scrollback == nil {
			scrollback = make(map[string]any)
		}
		scroll, _ := scrollback["scroll"].(map[string]any)
		if scroll == nil {
			scroll = make(map[string]any)
		}
		scroll["respect_manual_folds"] = enabled
		scrollback["scroll"] = scroll
		root["scrollback"] = scrollback
		return nil
	})
}
