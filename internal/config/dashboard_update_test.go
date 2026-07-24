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
	initial := "[ui]\ntheme = \"grokday\"\n\n[dashboard]\nenabled = false\npinned = [\" z \" , \"a\", \"a\", \"\"]\nreorder = [\" z \", \"a\", \"z\"]\ngrouping = \"dir\"\n"
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil || cfg.Dashboard.Enabled || !reflect.DeepEqual(cfg.Dashboard.Pinned, []string{"a", "z"}) || !reflect.DeepEqual(cfg.Dashboard.Reorder, []string{"z", "a"}) || cfg.Dashboard.Grouping != "directory" {
		t.Fatalf("load dashboard=%#v err=%v", cfg.Dashboard, err)
	}
	if err := UpdateDashboardPinned(path, []string{"b", " a ", "b", ""}); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(path)
	if err != nil || cfg.Dashboard.Enabled || !reflect.DeepEqual(cfg.Dashboard.Pinned, []string{"a", "b"}) || !reflect.DeepEqual(cfg.Dashboard.Reorder, []string{"z", "a"}) || cfg.Dashboard.Grouping != "directory" {
		t.Fatalf("reload dashboard=%#v err=%v", cfg.Dashboard, err)
	}
	if err := UpdateDashboardReorder(path, []string{"b", " a ", "b", ""}); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(path)
	if err != nil || cfg.Dashboard.Enabled || !reflect.DeepEqual(cfg.Dashboard.Pinned, []string{"a", "b"}) || !reflect.DeepEqual(cfg.Dashboard.Reorder, []string{"b", "a"}) || cfg.Dashboard.Grouping != "directory" {
		t.Fatalf("reorder dashboard=%#v err=%v", cfg.Dashboard, err)
	}
	if err := UpdateDashboardGrouping(path, "state"); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(path)
	if err != nil || cfg.Dashboard.Enabled || cfg.Dashboard.Grouping != "state" || !reflect.DeepEqual(cfg.Dashboard.Pinned, []string{"a", "b"}) || !reflect.DeepEqual(cfg.Dashboard.Reorder, []string{"b", "a"}) {
		t.Fatalf("grouping dashboard=%#v err=%v", cfg.Dashboard, err)
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), "theme = 'grokday'") && !strings.Contains(string(data), "theme = \"grokday\"") {
		t.Fatalf("config was not preserved: %s err=%v", data, err)
	}
}

func TestDashboardGroupingDefaultsToState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	for _, content := range []string{"", "[dashboard]\ngrouping = \"garbage\"\n"} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := Load(path)
		if err != nil || !cfg.Dashboard.Enabled || cfg.Dashboard.Grouping != "state" {
			t.Fatalf("content=%q dashboard=%#v err=%v", content, cfg.Dashboard, err)
		}
	}
}

func TestDashboardPersistenceKeepsSubagentReferences(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	ref := "sub:parent:child"
	if err := UpdateDashboardPinned(path, []string{ref, ref}); err != nil {
		t.Fatal(err)
	}
	if err := UpdateDashboardReorder(path, []string{ref}); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil || !reflect.DeepEqual(cfg.Dashboard.Pinned, []string{ref}) || !reflect.DeepEqual(cfg.Dashboard.Reorder, []string{ref}) {
		t.Fatalf("dashboard=%#v err=%v", cfg.Dashboard, err)
	}
}
