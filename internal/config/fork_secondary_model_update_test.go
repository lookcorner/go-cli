package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpdateForkSecondaryModelPreservesConfigurationAndClears(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[models]\ndefault = \"primary\"\n\n[ui]\ntheme = \"grokday\"\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := UpdateForkSecondaryModel(path, "secondary"); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil || cfg.UI.ForkSecondaryModel != "secondary" || cfg.UI.Theme != "grokday" || cfg.DefaultModelID != "primary" {
		t.Fatalf("config=%#v err=%v", cfg, err)
	}
	if err := UpdateForkSecondaryModel(path, " "); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(data), "fork_secondary_model") {
		t.Fatalf("config=%q err=%v", data, err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("mode info=%v err=%v", info, err)
	}
}

func TestUpdateForkSecondaryModelRejectsOversizedID(t *testing.T) {
	if err := UpdateForkSecondaryModel(filepath.Join(t.TempDir(), "config.toml"), strings.Repeat("x", 257)); err == nil {
		t.Fatal("oversized model id was accepted")
	}
}
