package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDashboardPinnedConfigRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	initial := "[ui]\ntheme = \"grokday\"\n\n[dashboard]\npinned = [\" z \" , \"a\", \"a\", \"\"]\n"
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil || !reflect.DeepEqual(cfg.Dashboard.Pinned, []string{"a", "z"}) {
		t.Fatalf("load pinned=%v err=%v", cfg.Dashboard.Pinned, err)
	}
	if err := UpdateDashboardPinned(path, []string{"b", " a ", "b", ""}); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(path)
	if err != nil || !reflect.DeepEqual(cfg.Dashboard.Pinned, []string{"a", "b"}) {
		t.Fatalf("reload pinned=%v err=%v", cfg.Dashboard.Pinned, err)
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), "theme = 'grokday'") && !strings.Contains(string(data), "theme = \"grokday\"") {
		t.Fatalf("config was not preserved: %s err=%v", data, err)
	}
}
