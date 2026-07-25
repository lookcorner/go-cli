package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpdateDefaultModelPreservesConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[models]\ndefault = \"old\"\nweb_search = \"search\"\n\n[model.old]\nmodel = \"old-api\"\n\n[model.new]\nmodel = \"new-api\"\n\n[model.search]\nmodel = \"search-api\"\n\n[ui]\nvim_mode = true\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := UpdateDefaultModel(path, "new"); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultModelID != "new" || cfg.Model != "new-api" || cfg.WebSearch.Model != "search-api" || !cfg.UI.VimMode {
		t.Fatalf("config=%#v", cfg)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("mode=%v", info.Mode().Perm())
	}
}

func TestUpdateDefaultModelClearsOverride(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[models]\ndefault = \"old\"\nweb_search = \"search\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := UpdateDefaultModel(path, " "); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(data), "default") || !strings.Contains(string(data), "web_search") {
		t.Fatalf("config=%q err=%v", data, err)
	}
}
