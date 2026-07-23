package config

import (
	"os"
	"path/filepath"

	"github.com/lookcorner/go-cli/internal/workspace"
	"github.com/pelletier/go-toml/v2"
)

// DiscoverEnvironment overlays trusted project config from the repository root to the workspace.
func DiscoverEnvironment(workspaceRoot string, cfg Config, projectTrusted bool) map[string]string {
	result := cloneEnvironment(cfg.Env)
	if !projectTrusted {
		return result
	}
	workspaceRoot = canonicalOrClean(workspaceRoot)
	root := workspace.GitRoot(workspaceRoot)
	for _, scope := range workspace.ProjectScopes(root, workspaceRoot) {
		data, err := os.ReadFile(filepath.Join(scope, ".grok", "config.toml"))
		if err != nil {
			continue
		}
		var file struct {
			Env map[string]string `toml:"env"`
		}
		if toml.Unmarshal(data, &file) != nil {
			continue
		}
		for key, value := range file.Env {
			result[key] = value
		}
	}
	return result
}

func cloneEnvironment(source map[string]string) map[string]string {
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}
