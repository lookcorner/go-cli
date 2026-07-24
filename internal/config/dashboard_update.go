package config

import (
	"sort"
	"strings"
)

func UpdateDashboardPinned(path string, ids []string) error {
	ids = cleanDashboardRefs(ids)
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

func UpdateDashboardReorder(path string, ids []string) error {
	ids = cleanDashboardRefOrder(ids)
	return updateUserConfig(path, func(root map[string]any) error {
		dashboard, _ := root["dashboard"].(map[string]any)
		if dashboard == nil {
			dashboard = make(map[string]any)
		}
		dashboard["reorder"] = ids
		root["dashboard"] = dashboard
		return nil
	})
}

func UpdateDashboardGrouping(path, grouping string) error {
	grouping = normalizeDashboardGrouping(grouping)
	return updateUserConfig(path, func(root map[string]any) error {
		dashboard, _ := root["dashboard"].(map[string]any)
		if dashboard == nil {
			dashboard = make(map[string]any)
		}
		dashboard["grouping"] = grouping
		root["dashboard"] = dashboard
		return nil
	})
}

func normalizeDashboardGrouping(grouping string) string {
	switch strings.ToLower(strings.TrimSpace(grouping)) {
	case "directory", "dir":
		return "directory"
	default:
		return "state"
	}
}

func cleanDashboardRefs(ids []string) []string {
	cleaned := cleanDashboardRefOrder(ids)
	sort.Strings(cleaned)
	return cleaned
}

func cleanDashboardRefOrder(ids []string) []string {
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
	return cleaned
}
