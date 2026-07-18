package config

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/lookcorner/go-cli/internal/compat"
	"github.com/lookcorner/go-cli/internal/plugin"
)

func TestMCPWatchPathsIncludesActiveDiscoverySources(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)
	configPath := filepath.Join(home, ".grok", "config.toml")
	pluginPath := filepath.Join(root, "plugin", ".mcp.json")
	disabledPath := filepath.Join(root, "disabled", ".mcp.json")
	paths := MCPWatchPaths(root, configPath, Config{Compat: compat.Default()}, []plugin.Plugin{
		{Enabled: true, MCPConfig: pluginPath},
		{Enabled: false, MCPConfig: disabledPath},
	}, true)
	for _, expected := range []string{
		configPath,
		filepath.Join(root, ".grok", "config.toml"),
		filepath.Join(root, ".mcp.json"),
		filepath.Join(root, ".cursor", "mcp.json"),
		filepath.Join(home, ".cursor", "mcp.json"),
		filepath.Join(home, ".claude.json"),
		pluginPath,
	} {
		if !slices.Contains(paths, expected) {
			t.Fatalf("missing MCP watch path %q in %#v", expected, paths)
		}
	}
	if slices.Contains(paths, disabledPath) {
		t.Fatalf("disabled plugin path was watched: %#v", paths)
	}
}
