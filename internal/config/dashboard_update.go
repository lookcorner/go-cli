package config

import (
	"sort"
	"strings"
)

func UpdateDashboardPinned(path string, ids []string) error {
	ids = cleanDashboardSessionIDs(ids)
	return updateUserConfig(path, func(root map[string]any) error {
		dashboard, _ := root["dashboard"].(map[string]any)
		if dashboard == nil {
			dashboard = make(map[string]any)
		}
		dashboard["pinned"] = ids
		root["dashboard"] = dashboard
		return nil
	})
}

func cleanDashboardSessionIDs(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
	cleaned := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		cleaned = append(cleaned, id)
	}
	sort.Strings(cleaned)
	return cleaned
}
