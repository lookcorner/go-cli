package config

import (
	"fmt"
	"strings"
)

func ReleaseChannel(path string) (string, error) {
	if path == "" {
		var err error
		path, err = discoverDefaultPath()
		if err != nil {
			return "", err
		}
	}
	root, err := readConfigMap(path)
	if err != nil {
		return "", err
	}
	cli, _ := root["cli"].(map[string]any)
	channel, _ := cli["channel"].(string)
	channel = strings.ToLower(strings.TrimSpace(channel))
	if channel == "" {
		return "stable", nil
	}
	if !validReleaseChannel(channel) {
		return "", fmt.Errorf("invalid release channel %q", channel)
	}
	return channel, nil
}

func UpdateReleaseChannel(path, channel string) error {
	channel = strings.ToLower(strings.TrimSpace(channel))
	if !validReleaseChannel(channel) {
		return fmt.Errorf("invalid release channel %q", channel)
	}
	return updateUserConfig(path, func(root map[string]any) error {
		cli, _ := root["cli"].(map[string]any)
		if cli == nil {
			cli = make(map[string]any)
		}
		cli["channel"] = channel
		root["cli"] = cli
		return nil
	})
}

func validReleaseChannel(channel string) bool {
	return channel == "stable" || channel == "alpha" || channel == "enterprise"
}
