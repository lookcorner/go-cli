package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCancelSubagentsOnTurnCancelLoadsAndNormalizes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[ui]\ncancel_subagents_on_turn_cancel = \" ALWAYS_STOP \"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil || cfg.UI.CancelSubagents != "always_stop" {
		t.Fatalf("policy=%q err=%v", cfg.UI.CancelSubagents, err)
	}
}

func TestUpdateCancelSubagentsOnTurnCancelPreservesAndClears(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[ui]\ntheme = \"grokday\"\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := UpdateCancelSubagentsOnTurnCancel(path, "always_continue"); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil || cfg.UI.CancelSubagents != "always_continue" || cfg.UI.Theme != "grokday" {
		t.Fatalf("config=%#v err=%v", cfg.UI, err)
	}
	if err := UpdateCancelSubagentsOnTurnCancel(path, "ask"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(data), "cancel_subagents_on_turn_cancel") {
		t.Fatalf("config=%q err=%v", data, err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("mode info=%v err=%v", info, err)
	}
}

func TestUpdateCancelSubagentsOnTurnCancelRejectsUnknown(t *testing.T) {
	if err := UpdateCancelSubagentsOnTurnCancel(filepath.Join(t.TempDir(), "config.toml"), "stop"); err == nil {
		t.Fatal("unknown policy was accepted")
	}
}
