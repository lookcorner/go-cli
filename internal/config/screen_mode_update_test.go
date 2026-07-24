package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpdateScreenModePreservesConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[ui]\ntheme = \"grokday\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := UpdateScreenMode(path, "minimal"); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil || cfg.UI.ScreenMode != "minimal" || cfg.UI.Theme != "grokday" {
		t.Fatalf("config=%#v err=%v", cfg.UI, err)
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), `screen_mode = 'minimal'`) {
		t.Fatalf("config=%q err=%v", data, err)
	}
	if err := UpdateScreenMode(path, "inline"); err == nil {
		t.Fatal("invalid screen mode was accepted")
	}
}
