package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lookcorner/go-cli/internal/plugin"
)

func TestDiscoverLSPServersMergesSourcesByPriority(t *testing.T) {
	root := canonicalOrClean(t.TempDir())
	home := t.TempDir()
	t.Setenv("GROK_HOME", "")
	writeLSPFile(t, filepath.Join(home, ".grok", "lsp.json"), `{"shared":{"command":"user"},"user":{"command":"user-only"}}`)
	writeLSPFile(t, filepath.Join(root, ".grok", "lsp.json"), `{"shared":{"command":"project"},"project":{"command":"project-only"}}`)
	pluginRoot := filepath.Join(root, "plugin")
	writeLSPFile(t, filepath.Join(pluginRoot, ".lsp.json"), `{"shared":{"command":"plugin"},"plugin":{"command":"plugin-only","extensions":{".go":"go"},"initializationOptions":{"x":true},"workspaceFolder":"backend","startupTimeout":1200,"shutdownTimeout":300,"restartOnCrash":true,"maxRestarts":4}}`)
	cfg := Config{LSPServers: map[string]LSPServerConfig{
		"shared": {Command: "config"},
		"config": {Command: "config-only"},
	}}
	servers := discoverLSPServers(root, home, cfg, []plugin.Plugin{{
		LSPConfig: filepath.Join(pluginRoot, ".lsp.json"), Executable: true,
	}}, true)
	for name, command := range map[string]string{
		"shared": "project", "config": "config-only", "user": "user-only",
		"project": "project-only", "plugin": "plugin-only",
	} {
		if servers[name].Command != command {
			t.Errorf("server %q command=%q want=%q", name, servers[name].Command, command)
		}
	}
	pluginServer := servers["plugin"]
	if strings.Join(pluginServer.Extensions, "|") != ".go" || pluginServer.InitializationOptions["x"] != true || pluginServer.WorkspaceFolder != "backend" {
		t.Fatalf("plugin aliases=%#v", pluginServer)
	}
	if pluginServer.StartupTimeoutMS != 1200 || pluginServer.ShutdownTimeoutMS != 300 || !pluginServer.RestartOnCrash || pluginServer.MaxRestarts == nil || *pluginServer.MaxRestarts != 4 {
		t.Fatalf("plugin lifecycle=%#v", pluginServer)
	}
}

func TestDiscoverLSPServersBlocksUntrustedProjectSources(t *testing.T) {
	root := canonicalOrClean(t.TempDir())
	home := t.TempDir()
	t.Setenv("GROK_HOME", "")
	writeLSPFile(t, filepath.Join(home, ".grok", "lsp.json"), `{"shared":{"command":"user"},"user":{"command":"user-only"}}`)
	writeLSPFile(t, filepath.Join(root, ".grok", "lsp.json"), `{"shared":{"command":"project"},"project":{"command":"project-only"}}`)
	pluginRoot := filepath.Join(root, "plugin")
	writeLSPFile(t, filepath.Join(pluginRoot, ".lsp.json"), `{"plugin":{"command":"plugin"}}`)
	servers := discoverLSPServers(root, home, Config{}, []plugin.Plugin{{
		LSPConfig: filepath.Join(pluginRoot, ".lsp.json"), Executable: false,
	}}, false)
	if servers["shared"].Command != "user" || servers["user"].Command != "user-only" {
		t.Fatalf("user LSP config was blocked: %#v", servers)
	}
	for _, name := range []string{"project", "plugin"} {
		if _, ok := servers[name]; ok {
			t.Fatalf("untrusted LSP server %q was loaded", name)
		}
	}
}

func TestDiscoverLSPServersUsesInlinePluginConfig(t *testing.T) {
	root := t.TempDir()
	servers := discoverLSPServers(root, t.TempDir(), Config{}, []plugin.Plugin{{
		InlineLSP:  json.RawMessage(`{"inline":{"command":"inline","extensionToLanguageId":{".ts":"typescript"}}}`),
		Executable: true,
	}}, true)
	if servers["inline"].Command != "inline" || strings.Join(servers["inline"].Extensions, "|") != ".ts" {
		t.Fatalf("inline LSP config=%#v", servers)
	}
}

func writeLSPFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
