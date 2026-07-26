package config

import (
	"fmt"
	"path/filepath"
)

type PagerScroll struct {
	AnchorOnFold       bool
	FollowIndicator    string
	FollowByOverscroll bool
	RespectManualFolds bool
}

func PagerPath() (string, error) {
	home, err := PolicyHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "pager.toml"), nil
}

func LoadPagerScroll(path string) (PagerScroll, error) {
	settings := PagerScroll{AnchorOnFold: true, FollowIndicator: "center", FollowByOverscroll: true}
	root, err := readConfigMap(path)
	if err != nil {
		return settings, err
	}
	scrollback, _ := root["scrollback"].(map[string]any)
	scroll, _ := scrollback["scroll"].(map[string]any)
	if enabled, ok := scroll["anchor_on_fold"].(bool); ok {
		settings.AnchorOnFold = enabled
	}
	if indicator, ok := scroll["follow_indicator"].(string); ok {
		if indicator != "center" && indicator != "none" {
			return settings, fmt.Errorf("invalid scrollback.scroll.follow_indicator %q", indicator)
		}
		settings.FollowIndicator = indicator
	}
	if enabled, ok := scroll["follow_by_overscroll"].(bool); ok {
		settings.FollowByOverscroll = enabled
	}
	settings.RespectManualFolds, _ = scroll["respect_manual_folds"].(bool)
	return settings, nil
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
