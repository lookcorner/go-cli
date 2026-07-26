package tips

import (
	"encoding/json"
	"os"
	"path/filepath"
)

type Source struct {
	Items          []string `json:"tips" toml:"tips"`
	ExcludeDefault bool     `json:"exclude_default" toml:"exclude_default"`
}

func Merge(requirements, user, managed Source, remote []string) []string {
	excludeDefault := requirements.ExcludeDefault || user.ExcludeDefault || managed.ExcludeDefault
	result := make([]string, 0, len(requirements.Items)+len(remote)+len(user.Items)+len(managed.Items))
	result = append(result, requirements.Items...)
	if !excludeDefault {
		result = append(result, remote...)
	}
	result = append(result, user.Items...)
	result = append(result, managed.Items...)
	return result
}

func PickAndAdvance(items []string, home string) string {
	if len(items) == 0 {
		return ""
	}
	path := filepath.Join(home, "tip_cursor.json")
	var state struct {
		Cursor uint64 `json:"cursor"`
	}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &state)
	}
	item := items[state.Cursor%uint64(len(items))]
	state.Cursor++
	if data, err := json.Marshal(state); err == nil {
		_ = os.MkdirAll(home, 0o700)
		_ = os.WriteFile(path, data, 0o600)
	}
	return item
}
