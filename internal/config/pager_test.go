package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRespectManualFoldsPagerConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	path, err := PagerPath()
	if err != nil || path != filepath.Join(home, "pager.toml") {
		t.Fatalf("path=%q err=%v", path, err)
	}
	if enabled, err := LoadRespectManualFolds(path); err != nil || enabled {
		t.Fatalf("default=%v err=%v", enabled, err)
	}
	if err := os.WriteFile(path, []byte("[other]\nvalue = 1\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := UpdateRespectManualFolds(path, true); err != nil {
		t.Fatal(err)
	}
	enabled, err := LoadRespectManualFolds(path)
	if err != nil || !enabled {
		t.Fatalf("enabled=%v err=%v", enabled, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "value = 1") || !strings.Contains(text, "respect_manual_folds = true") {
		t.Fatalf("pager config=%q", text)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("mode=%v", info.Mode().Perm())
	}
}
