package version

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/mod/semver"
)

var Current = "0.1.0-dev"

func Channel() string {
	home := strings.TrimSpace(os.Getenv("GROK_HOME"))
	if home == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return "unknown"
		}
		home = filepath.Join(userHome, ".grok")
	}
	data, err := os.ReadFile(filepath.Join(home, "version.json"))
	if err != nil {
		return "unknown"
	}
	var cache struct {
		StableVersion string `json:"stable_version"`
	}
	if json.Unmarshal(data, &cache) != nil {
		return "unknown"
	}
	current, stable := "v"+strings.TrimPrefix(Current, "v"), "v"+strings.TrimPrefix(cache.StableVersion, "v")
	if !semver.IsValid(current) || !semver.IsValid(stable) {
		return "unknown"
	}
	if semver.Compare(current, stable) > 0 {
		return "alpha"
	}
	return "stable"
}
