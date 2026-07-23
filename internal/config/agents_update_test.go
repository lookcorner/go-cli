package config

import (
	"path/filepath"
	"testing"
)

func TestAgentSettingsLifecyclePreservesOtherConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := UpdateAgentToggle(path, "explore", false); err != nil {
		t.Fatal(err)
	}
	if err := UpdateDefaultAgent(path, "plan"); err != nil {
		t.Fatal(err)
	}
	settings, err := LoadAgentSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	if settings.Toggle["explore"] || settings.Default != "plan" {
		t.Fatalf("settings=%#v", settings)
	}
	if err := UpdateDefaultAgent(path, ""); err != nil {
		t.Fatal(err)
	}
	settings, err = LoadAgentSettings(path)
	if err != nil || settings.Default != "" || settings.Toggle["explore"] {
		t.Fatalf("cleared settings=%#v err=%v", settings, err)
	}
}
