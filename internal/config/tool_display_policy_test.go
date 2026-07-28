package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestToolDisplayPolicyPrecedence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[ui]\ngroup_tool_verbs = false\ncollapsed_edit_blocks = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GROK_GROUP_TOOL_VERBS", "true")
	t.Setenv("GROK_COLLAPSED_EDIT_BLOCKS", "false")

	cfg, err := Load(path)
	if err != nil || !cfg.UI.GroupToolVerbs || cfg.UI.CollapsedEditBlocks {
		t.Fatalf("environment policy=%#v err=%v", cfg.UI, err)
	}
	cfg.ApplyRemoteSettings(&RemoteSettings{
		GroupToolVerbs:      boolPointer(false),
		CollapsedEditBlocks: boolPointer(true),
	})
	if !cfg.UI.GroupToolVerbs || cfg.UI.CollapsedEditBlocks {
		t.Fatalf("remote policy overrode environment: %#v", cfg.UI)
	}

	if err := applyRequirementsData(&cfg, []byte("[ui]\ngroup_tool_verbs = false\ncollapsed_edit_blocks = true\n"), "test", false, false); err != nil {
		t.Fatal(err)
	}
	if cfg.UI.GroupToolVerbs || !cfg.UI.CollapsedEditBlocks {
		t.Fatalf("requirements policy=%#v", cfg.UI)
	}
}
