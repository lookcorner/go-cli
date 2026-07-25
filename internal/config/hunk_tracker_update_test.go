package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpdateHunkTrackerModePreservesConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[models]\ndefault = \"local\"\n\n[ui]\ntheme = \"grokday\"\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := UpdateHunkTrackerMode(path, "all_dirty"); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil || cfg.UI.HunkTrackerMode != "all_dirty" || cfg.UI.Theme != "grokday" || cfg.DefaultModelID != "local" {
		t.Fatalf("config=%#v err=%v", cfg, err)
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), `hunk_tracker_mode = 'all_dirty'`) {
		t.Fatalf("config=%q err=%v", data, err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != wantConfigPerm(0o640) {
		t.Fatalf("mode info=%v err=%v", info, err)
	}
	if err := UpdateHunkTrackerMode(path, "disabled"); err == nil {
		t.Fatal("invalid hunk tracker mode was accepted")
	}
}
