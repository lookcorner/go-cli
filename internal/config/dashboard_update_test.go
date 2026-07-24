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
	initial := "[ui]\ntheme = \"grokday\"\n\n[dashboard]\npinned = [\" z \" , \"a\", \"a\", \"\"]\nreorder = [\" z \", \"a\", \"z\"]\n"
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil || !reflect.DeepEqual(cfg.Dashboard.Pinned, []string{"a", "z"}) || !reflect.DeepEqual(cfg.Dashboard.Reorder, []string{"z", "a"}) {
		t.Fatalf("load dashboard=%#v err=%v", cfg.Dashboard, err)
	}
	if err := UpdateDashboardPinned(path, []string{"b", " a ", "b", ""}); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(path)
	if err != nil || !reflect.DeepEqual(cfg.Dashboard.Pinned, []string{"a", "b"}) || !reflect.DeepEqual(cfg.Dashboard.Reorder, []string{"z", "a"}) {
		t.Fatalf("reload dashboard=%#v err=%v", cfg.Dashboard, err)
	}
	if err := UpdateDashboardReorder(path, []string{"b", " a ", "b", ""}); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(path)
	if err != nil || !reflect.DeepEqual(cfg.Dashboard.Pinned, []string{"a", "b"}) || !reflect.DeepEqual(cfg.Dashboard.Reorder, []string{"b", "a"}) {
		t.Fatalf("reorder dashboard=%#v err=%v", cfg.Dashboard, err)
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), "theme = 'grokday'") && !strings.Contains(string(data), "theme = \"grokday\"") {
		t.Fatalf("config was not preserved: %s err=%v", data, err)
	}
}
