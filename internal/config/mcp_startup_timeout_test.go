package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMCPStartupTimeoutPrecedenceAndServerOverride(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	path := filepath.Join(home, "config.toml")
	if err := os.WriteFile(path, []byte("[mcp]\nstartup_timeout_sec = 40\n\n[mcp_servers.fixture]\ncommand = \"fixture\"\nstartup_timeout_sec = 9\ntool_timeout_sec = 12\ntool_timeouts = { search = 3 }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MCP_TIMEOUT", "1501")
	t.Setenv("GROK_MCP_STARTUP_TIMEOUT_SECS", "3")

	cfg, err := Load(path)
	if err != nil || cfg.MCP.StartupTimeoutSeconds != 2 {
		t.Fatalf("environment timeout=%d err=%v", cfg.MCP.StartupTimeoutSeconds, err)
	}
	if timeout := cfg.MCPServers["fixture"].StartupTimeoutSeconds; timeout == nil || *timeout != 9 {
		t.Fatalf("per-server timeout=%v", timeout)
	}
	server := cfg.MCPServers["fixture"]
	if server.ToolTimeoutSeconds == nil || *server.ToolTimeoutSeconds != 12 || server.ToolTimeouts["search"] != 3 {
		t.Fatalf("per-server tool timeouts=%#v", server)
	}
	if err := os.WriteFile(filepath.Join(home, "requirements.toml"), []byte("[mcp]\nstartup_timeout_sec = 4\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(path)
	if err != nil || cfg.MCP.StartupTimeoutSeconds != 4 {
		t.Fatalf("requirements timeout=%d err=%v", cfg.MCP.StartupTimeoutSeconds, err)
	}
}

func TestMCPStartupTimeoutFallbacks(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	t.Setenv("MCP_TIMEOUT", "invalid")
	t.Setenv("GROK_MCP_STARTUP_TIMEOUT_SECS", "6")
	cfg, err := Load(filepath.Join(home, "missing.toml"))
	if err != nil || cfg.MCP.StartupTimeoutSeconds != 6 {
		t.Fatalf("native environment timeout=%d err=%v", cfg.MCP.StartupTimeoutSeconds, err)
	}

	t.Setenv("GROK_MCP_STARTUP_TIMEOUT_SECS", "")
	cfg, err = Load(filepath.Join(home, "missing.toml"))
	if err != nil || cfg.MCP.StartupTimeoutSeconds != 30 {
		t.Fatalf("default timeout=%d err=%v", cfg.MCP.StartupTimeoutSeconds, err)
	}
	remote := uint64(7)
	cfg.ApplyRemoteSettings(&RemoteSettings{MCPStartupTimeoutSeconds: &remote})
	if cfg.MCP.StartupTimeoutSeconds != 7 {
		t.Fatalf("remote timeout=%d", cfg.MCP.StartupTimeoutSeconds)
	}
	cfg.ApplyRemoteSettings(&RemoteSettings{})
	if cfg.MCP.StartupTimeoutSeconds != 30 {
		t.Fatalf("cleared remote timeout=%d", cfg.MCP.StartupTimeoutSeconds)
	}
}
