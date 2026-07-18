package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/lookcorner/go-cli/internal/compat"
	"github.com/lookcorner/go-cli/internal/plugin"
)

func TestDiscoverMCPServersMergesSourcesByPriority(t *testing.T) {
	root := canonicalOrClean(t.TempDir())
	home := t.TempDir()
	t.Setenv("MCP_TOKEN", "secret")
	writeMCPFile(t, filepath.Join(root, ".grok", "config.toml"), `[mcp_servers.shared]
command = "project"
[mcp_servers.project]
command = "project-only"
`)
	writeMCPFile(t, filepath.Join(root, ".mcp.json"), `{"mcpServers":{"shared":{"command":"local"},"local":{"command":"local-only"}}}`)
	writeMCPFile(t, filepath.Join(root, ".cursor", "mcp.json"), `{"mcpServers":{"shared":{"command":"cursor-project"},"cursor":{"command":"cursor-project"}}}`)
	writeMCPFile(t, filepath.Join(home, ".cursor", "mcp.json"), `{"mcpServers":{"cursor":{"command":"cursor-global"},"cursor-global":{"command":"cursor-global"}}}`)
	writeMCPFile(t, filepath.Join(home, ".claude.json"), `{"mcpServers":{"shared":{"command":"claude-user"},"claude-user":{"command":"claude-user"}},"projects":{"`+root+`":{"mcpServers":{"claude":{"command":"claude-project"}}}}}`)
	pluginRoot := filepath.Join(root, "plugin")
	pluginData := filepath.Join(home, ".grok", "plugin-data", "p")
	writeMCPFile(t, filepath.Join(pluginRoot, ".mcp.json"), `{"mcpServers":{"shared":{"command":"plugin"},"plugin":{"command":"${GROK_PLUGIN_ROOT}/bin","args":["$MCP_TOKEN","$UNKNOWN"],"env":{"DATA":"${GROK_PLUGIN_DATA}"}}}}`)

	cfg := Config{
		Compat: compat.Default(),
		MCPServers: map[string]MCPServerConfig{
			"global": {Command: "global"},
			"shared": {Command: "global-shared"},
		},
	}
	servers := discoverMCPServers(root, home, cfg, []plugin.Plugin{{
		Name: "p", Root: pluginRoot, DataDir: pluginData,
		MCPConfig: filepath.Join(pluginRoot, ".mcp.json"), Executable: true,
	}}, true)

	for name, command := range map[string]string{
		"shared": "project", "global": "global", "project": "project-only",
		"plugin": filepath.Join(pluginRoot, "bin"), "claude": "claude-project",
		"claude-user": "claude-user", "cursor": "cursor-project",
		"cursor-global": "cursor-global", "local": "local-only",
	} {
		if servers[name].Command != command {
			t.Errorf("server %q command=%q want=%q", name, servers[name].Command, command)
		}
	}
	if got := servers["plugin"]; len(got.Args) != 2 || got.Args[0] != "secret" || got.Args[1] != "$UNKNOWN" || got.Env["DATA"] != pluginData {
		t.Fatalf("plugin substitutions=%#v", got)
	}
}

func TestDiscoverMCPServersHonorsCompat(t *testing.T) {
	root := canonicalOrClean(t.TempDir())
	home := t.TempDir()
	writeMCPFile(t, filepath.Join(root, ".cursor", "mcp.json"), `{"mcpServers":{"cursor":{"command":"cursor"}}}`)
	writeMCPFile(t, filepath.Join(home, ".claude.json"), `{"mcpServers":{"claude":{"command":"claude"}}}`)
	cfg := Config{Compat: compat.Default()}
	cfg.Compat.Cursor.Mcps = false
	cfg.Compat.Claude.Mcps = false
	servers := discoverMCPServers(root, home, cfg, nil, true)
	if _, ok := servers["cursor"]; ok {
		t.Fatal("disabled Cursor MCP compatibility was loaded")
	}
	if _, ok := servers["claude"]; ok {
		t.Fatal("disabled Claude MCP compatibility was loaded")
	}
}

func TestDiscoverMCPServersClosestMCPJSONWins(t *testing.T) {
	root := t.TempDir()
	if err := exec.Command("git", "init", "-q", root).Run(); err != nil {
		t.Skipf("git unavailable: %v", err)
	}
	nested := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	writeMCPFile(t, filepath.Join(root, ".mcp.json"), `{"mcpServers":{"shared":{"command":"root"}}}`)
	writeMCPFile(t, filepath.Join(nested, ".mcp.json"), `{"mcpServers":{"shared":{"command":"nested"}}}`)
	servers := discoverMCPServers(nested, t.TempDir(), Config{Compat: compat.Default()}, nil, true)
	if servers["shared"].Command != "nested" {
		t.Fatalf("closest .mcp.json did not win: %#v", servers["shared"])
	}
}

func TestDiscoverMCPServersBlocksUntrustedProjectSources(t *testing.T) {
	root := canonicalOrClean(t.TempDir())
	home := t.TempDir()
	writeMCPFile(t, filepath.Join(root, ".mcp.json"), `{"mcpServers":{"project":{"command":"project"}}}`)
	writeMCPFile(t, filepath.Join(root, ".cursor", "mcp.json"), `{"mcpServers":{"cursor-project":{"command":"project"}}}`)
	writeMCPFile(t, filepath.Join(home, ".cursor", "mcp.json"), `{"mcpServers":{"cursor-user":{"command":"user"}}}`)
	writeMCPFile(t, filepath.Join(home, ".claude.json"), `{"mcpServers":{"claude-user":{"command":"user"}},"projects":{"`+root+`":{"mcpServers":{"claude-project":{"command":"project"}}}}}`)
	servers := discoverMCPServers(root, home, Config{Compat: compat.Default(), MCPServers: map[string]MCPServerConfig{
		"project": {Command: "global-fallback"},
	}}, []plugin.Plugin{{
		MCPConfig: filepath.Join(root, ".mcp.json"), Executable: false,
	}}, false)
	for _, name := range []string{"project", "cursor-project", "claude-project"} {
		if _, ok := servers[name]; ok {
			t.Fatalf("untrusted project server %q was loaded", name)
		}
	}
	for _, name := range []string{"cursor-user", "claude-user"} {
		if _, ok := servers[name]; !ok {
			t.Fatalf("user server %q was blocked with project sources", name)
		}
	}
}

func writeMCPFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
