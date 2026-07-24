package config

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestMCPServerConfigLifecyclePreservesOtherSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[models]\ndefault = \"local\"\n\n[mcp_servers.keep]\ncommand = \"keep\"\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := UpsertMCPServer(path, "added", MCPServerConfig{URL: "https://mcp.example", Type: "http", Headers: map[string]string{"X-Test": "yes"}}); err != nil {
		t.Fatal(err)
	}
	if err := SetMCPServerEnabled(path, "added", false); err != nil {
		t.Fatal(err)
	}
	if err := SetMCPToolEnabled(path, "added", "search", false); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MCPServers["keep"].Command != "keep" || cfg.MCPServers["added"].URL != "https://mcp.example" || !slices.Contains(cfg.DisabledMCPServers, "added") || !slices.Equal(cfg.DisabledMCPTools["added"], []string{"search"}) {
		t.Fatalf("unexpected persisted MCP settings: %#v", cfg)
	}
	servers := discoverMCPServers(t.TempDir(), t.TempDir(), cfg, nil, true)
	if servers["added"].IsEnabled() {
		t.Fatalf("disabled MCP server remained enabled: %#v", servers["added"])
	}
	if err := SetMCPServerEnabled(path, "added", true); err != nil {
		t.Fatal(err)
	}
	if err := SetMCPToolEnabled(path, "added", "search", true); err != nil {
		t.Fatal(err)
	}
	if err := SetMCPToolEnabled(path, "added", "search", false); err != nil {
		t.Fatal(err)
	}
	existed, err := DeleteMCPServer(path, "added")
	if err != nil || !existed {
		t.Fatalf("delete existed=%v err=%v", existed, err)
	}
	cfg, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := cfg.MCPServers["added"]; exists || len(cfg.DisabledMCPServers) != 0 || len(cfg.DisabledMCPTools) != 0 || cfg.MCPServers["keep"].Command != "keep" {
		t.Fatalf("delete damaged config: %#v", cfg)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("config mode=%v err=%v", info.Mode().Perm(), err)
	}
}

func TestMCPServerConfigLifecycleValidatesMutations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := UpsertMCPServer(path, "", MCPServerConfig{Command: "server"}); err == nil {
		t.Fatal("empty MCP server name was accepted")
	}
	if err := UpsertMCPServer(path, "empty", MCPServerConfig{}); err == nil {
		t.Fatal("empty MCP server config was accepted")
	}
	disabled := false
	if err := UpsertMCPServer(path, "disabled", MCPServerConfig{Command: "server", Enabled: &disabled}); err == nil {
		t.Fatal("disabled MCP server config was accepted")
	}
	if err := SetMCPToolEnabled(path, "", "tool", false); err == nil {
		t.Fatal("empty MCP tool server name was accepted")
	}
	if existed, err := DeleteMCPServer(path, "missing"); err != nil || existed {
		t.Fatalf("missing delete existed=%v err=%v", existed, err)
	}
}

func TestLoadMCPServersAtReadsOnlyRequestedLayer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("disabled_mcp_servers = [\"local\"]\n[mcp_servers.local]\ncommand = \"server\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	servers, disabled, err := LoadMCPServersAt(path)
	if err != nil {
		t.Fatal(err)
	}
	if servers["local"].Command != "server" || len(disabled) != 1 || disabled[0] != "local" {
		t.Fatalf("servers=%#v disabled=%#v", servers, disabled)
	}
}
