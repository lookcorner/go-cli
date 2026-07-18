package config

import (
	"os"
	"path/filepath"
	"sort"

	"github.com/lookcorner/go-cli/internal/plugin"
	"github.com/lookcorner/go-cli/internal/workspace"
)

// MCPWatchPaths returns every local file that can change MCP discovery for a
// workspace. Missing paths are included so later file creation is detected.
func MCPWatchPaths(workspaceRoot, configPath string, cfg Config, plugins []plugin.Plugin, projectTrusted bool) []string {
	paths := []string{configPath}
	home, _ := os.UserHomeDir()
	gitRoot := workspace.GitRoot(workspaceRoot)
	for _, scope := range workspace.ProjectScopes(gitRoot, workspaceRoot) {
		paths = append(paths, filepath.Join(scope, ".grok", "config.toml"), filepath.Join(scope, ".mcp.json"))
	}
	for _, item := range plugins {
		if item.Enabled {
			paths = append(paths, item.MCPConfig)
		}
	}
	if cfg.Compat.Claude.Mcps && home != "" {
		paths = append(paths, filepath.Join(home, ".claude.json"))
	}
	if cfg.Compat.Cursor.Mcps {
		if projectTrusted {
			paths = append(paths, filepath.Join(workspaceRoot, ".cursor", "mcp.json"))
		}
		if home != "" {
			paths = append(paths, filepath.Join(home, ".cursor", "mcp.json"))
		}
	}
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
