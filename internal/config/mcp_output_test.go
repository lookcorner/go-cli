package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMCPOutputLimitPrecedence(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	path := filepath.Join(home, "config.toml")
	if err := os.WriteFile(path, []byte("[mcp]\nmax_output_bytes = 12345\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil || cfg.MCP.MaxOutputBytes != 12_345 {
		t.Fatalf("local limit=%d err=%v", cfg.MCP.MaxOutputBytes, err)
	}
	remote := uint64(777)
	cfg.ApplyRemoteSettings(&RemoteSettings{MaxMCPOutputBytes: &remote})
	if cfg.MCP.MaxOutputBytes != 12_345 {
		t.Fatalf("remote overrode local limit: %d", cfg.MCP.MaxOutputBytes)
	}

	t.Setenv("GROK_MAX_MCP_OUTPUT_BYTES", "23456")
	cfg, err = Load(path)
	if err != nil || cfg.MCP.MaxOutputBytes != 23_456 {
		t.Fatalf("environment limit=%d err=%v", cfg.MCP.MaxOutputBytes, err)
	}
}

func TestMCPOutputLimitDefaultsAndRemoteRefresh(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	t.Setenv("GROK_MAX_MCP_OUTPUT_BYTES", "")
	t.Setenv("MAX_MCP_OUTPUT_BYTES", "")
	cfg, err := Load(filepath.Join(home, "missing.toml"))
	if err != nil || cfg.MCP.MaxOutputBytes != 20_000 {
		t.Fatalf("default limit=%d err=%v", cfg.MCP.MaxOutputBytes, err)
	}
	remote := uint64(321)
	cfg.ApplyRemoteSettings(&RemoteSettings{MaxMCPOutputBytes: &remote})
	if cfg.MCP.MaxOutputBytes != 321 {
		t.Fatalf("remote limit=%d", cfg.MCP.MaxOutputBytes)
	}
	cfg.ApplyRemoteSettings(&RemoteSettings{})
	if cfg.MCP.MaxOutputBytes != 20_000 {
		t.Fatalf("cleared remote limit=%d", cfg.MCP.MaxOutputBytes)
	}
}

func TestMCPOutputLimitRequirementsOverrideEnvironment(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	t.Setenv("GROK_MAX_MCP_OUTPUT_BYTES", "23456")
	if err := os.WriteFile(filepath.Join(home, "requirements.toml"), []byte("[mcp]\nmax_output_bytes = 34567\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(filepath.Join(home, "missing.toml"))
	if err != nil || cfg.MCP.MaxOutputBytes != 34_567 {
		t.Fatalf("requirements limit=%d err=%v", cfg.MCP.MaxOutputBytes, err)
	}
}
