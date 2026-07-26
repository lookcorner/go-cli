package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMaxThoughtsWidthDefaultsLoadsAndUpdates(t *testing.T) {
	defaultConfig, err := Load(filepath.Join(t.TempDir(), "missing.toml"))
	if err != nil || defaultConfig.UI.MaxThoughtsWidth != 120 {
		t.Fatalf("default=%d err=%v", defaultConfig.UI.MaxThoughtsWidth, err)
	}

	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[ui]\nmax_thoughts_width = 20\ntheme = 'grokday'\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil || cfg.UI.MaxThoughtsWidth != 40 {
		t.Fatalf("loaded=%d err=%v", cfg.UI.MaxThoughtsWidth, err)
	}
	if err := UpdateMaxThoughtsWidth(path, 900); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(path)
	if err != nil || cfg.UI.MaxThoughtsWidth != 500 || cfg.UI.Theme != "grokday" {
		t.Fatalf("updated=%#v err=%v", cfg.UI, err)
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), "max_thoughts_width = 500") || !strings.Contains(string(data), "theme = 'grokday'") {
		t.Fatalf("config=%q err=%v", data, err)
	}
}
