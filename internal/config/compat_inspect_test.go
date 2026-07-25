package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCompatibilitySettingsReportsEffectiveValuesAndSources(t *testing.T) {
	for _, name := range []string{
		"GROK_CURSOR_SKILLS_ENABLED", "GROK_CURSOR_RULES_ENABLED", "GROK_CURSOR_AGENTS_ENABLED", "GROK_CURSOR_MCPS_ENABLED", "GROK_CURSOR_HOOKS_ENABLED", "GROK_CURSOR_SESSIONS_ENABLED",
		"GROK_CLAUDE_SKILLS_ENABLED", "GROK_CLAUDE_RULES_ENABLED", "GROK_CLAUDE_AGENTS_ENABLED", "GROK_CLAUDE_MCPS_ENABLED", "GROK_CLAUDE_HOOKS_ENABLED", "GROK_CLAUDE_SESSIONS_ENABLED",
		"GROK_CODEX_SESSIONS_ENABLED",
	} {
		t.Setenv(name, "")
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[compat.cursor]\nrules = false\n[compat.claude]\nhooks = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GROK_CURSOR_SKILLS_ENABLED", "false")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cells := cfg.CompatibilitySettings()
	if len(cells) != 13 {
		t.Fatalf("cells=%#v", cells)
	}
	assertCompatibilitySetting(t, cells, "cursor", "skills", false, "env")
	assertCompatibilitySetting(t, cells, "cursor", "rules", false, "config")
	assertCompatibilitySetting(t, cells, "cursor", "agents", true, "default")
	assertCompatibilitySetting(t, cells, "claude", "hooks", false, "config")
	assertCompatibilitySetting(t, cells, "codex", "sessions", true, "default")
}

func assertCompatibilitySetting(t *testing.T, cells []CompatibilitySetting, vendor, surface string, enabled bool, source string) {
	t.Helper()
	for _, cell := range cells {
		if cell.Vendor == vendor && cell.Surface == surface {
			if cell.Enabled != enabled || cell.Source != source {
				t.Fatalf("%s.%s=%#v", vendor, surface, cell)
			}
			return
		}
	}
	t.Fatalf("missing %s.%s", vendor, surface)
}
