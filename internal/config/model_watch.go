package config

import "sort"

// ModelWatchPaths includes every local layer that can alter the model catalog.
func ModelWatchPaths(configPath string) []string {
	if configPath == "" {
		configPath, _ = discoverDefaultPath()
	}
	paths := append(managedConfigPaths(), configPath)
	paths = append(paths, requirementsPaths()...)
	sort.Strings(paths)
	unique := paths[:0]
	for _, path := range paths {
		if path == "" || len(unique) > 0 && unique[len(unique)-1] == path {
			continue
		}
		unique = append(unique, path)
	}
	return unique
}
