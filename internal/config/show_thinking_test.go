package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestShowThinkingBlocksPrecedence(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	path := filepath.Join(home, "config.toml")
	if err := os.WriteFile(path, []byte("[ui]\nshow_thinking_blocks = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GROK_SHOW_THINKING_BLOCKS", "false")
	if err := os.WriteFile(filepath.Join(home, "requirements.toml"), []byte("[ui]\nshow_thinking_blocks = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil || !cfg.UI.ShowThinkingBlocks {
		t.Fatalf("requirements thinking=%v err=%v", cfg.UI.ShowThinkingBlocks, err)
	}
	if err := os.Remove(filepath.Join(home, "requirements.toml")); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(path)
	if err != nil || cfg.UI.ShowThinkingBlocks {
		t.Fatalf("environment thinking=%v err=%v", cfg.UI.ShowThinkingBlocks, err)
	}
}

func TestShowThinkingBlocksRemoteFallback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	cfg, err := Load(filepath.Join(home, "missing.toml"))
	if err != nil {
		t.Fatal(err)
	}
	off := false
	cfg.ApplyRemoteSettings(&RemoteSettings{ShowThinkingBlocks: &off})
	if cfg.UI.ShowThinkingBlocks {
		t.Fatal("remote setting did not hide thinking blocks")
	}
	cfg.ApplyRemoteSettings(&RemoteSettings{})
	if !cfg.UI.ShowThinkingBlocks {
		t.Fatal("missing remote setting did not restore the default")
	}

	path := filepath.Join(home, "config.toml")
	if err := os.WriteFile(path, []byte("[ui]\nshow_thinking_blocks = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	on := true
	cfg.ApplyRemoteSettings(&RemoteSettings{ShowThinkingBlocks: &on})
	if cfg.UI.ShowThinkingBlocks {
		t.Fatal("remote setting overrode local thinking policy")
	}
}
