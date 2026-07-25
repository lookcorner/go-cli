package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestContextualUndoHintDefaultsAndLocalConfig(t *testing.T) {
	t.Setenv("GROK_HOME", t.TempDir())
	cfg, err := Load(filepath.Join(t.TempDir(), "missing.toml"))
	if err != nil || !cfg.UI.ContextualHints.Undo {
		t.Fatalf("default=%#v err=%v", cfg.UI.ContextualHints, err)
	}

	for _, test := range []struct {
		name, file, content string
	}{
		{name: "toml", file: "config.toml", content: "[ui.contextual_hints]\nundo = false\n"},
		{name: "json", file: "config.json", content: `{"ui":{"contextual_hints":{"undo":false}}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), test.file)
			if err := os.WriteFile(path, []byte(test.content), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(path)
			if err != nil || cfg.UI.ContextualHints.Undo {
				t.Fatalf("config=%#v err=%v", cfg.UI.ContextualHints, err)
			}
		})
	}
}

func TestContextualUndoHintRemoteAndLocalPrecedence(t *testing.T) {
	cfg := Config{UI: UIConfig{ContextualHints: Hints{Undo: true}}}
	cfg.ApplyRemoteSettings(&RemoteSettings{ContextualHints: &RemoteHints{Undo: boolPointer(false)}})
	if cfg.UI.ContextualHints.Undo {
		t.Fatal("remote false was ignored")
	}
	cfg.ApplyRemoteSettings(&RemoteSettings{})
	if !cfg.UI.ContextualHints.Undo {
		t.Fatal("missing remote value did not restore the default")
	}

	for _, local := range []bool{false, true} {
		cfg = Config{
			UI:                         UIConfig{ContextualHints: Hints{Undo: local}},
			uiContextualUndoConfigured: true,
		}
		cfg.ApplyRemoteSettings(&RemoteSettings{ContextualHints: &RemoteHints{Undo: boolPointer(!local)}})
		if cfg.UI.ContextualHints.Undo != local {
			t.Fatalf("local=%v resolved=%v", local, cfg.UI.ContextualHints.Undo)
		}
	}
}

func TestUpdateContextualUndoHintPreservesConfig(t *testing.T) {
	for _, test := range []struct {
		name, file, content string
	}{
		{name: "toml", file: "config.toml", content: "model_name = \"test\"\n[ui.contextual_hints]\nplan_mode = false\n"},
		{name: "json", file: "config.json", content: `{"model_name":"test","ui":{"contextual_hints":{"plan_mode":false}}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), test.file)
			if err := os.WriteFile(path, []byte(test.content), 0o640); err != nil {
				t.Fatal(err)
			}
			if err := UpdateContextualUndoHint(path, false); err != nil {
				t.Fatal(err)
			}
			root, err := readConfigMap(path)
			if err != nil {
				t.Fatal(err)
			}
			ui := root["ui"].(map[string]any)
			hints := ui["contextual_hints"].(map[string]any)
			if root["model_name"] != "test" || hints["plan_mode"] != false || hints["undo"] != false {
				data, _ := json.Marshal(root)
				t.Fatalf("config=%s", data)
			}
			if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o640 {
				t.Fatalf("mode=%v err=%v", info, err)
			}
		})
	}
}

func TestRemoteSettingsDecodesContextualUndoHint(t *testing.T) {
	var settings RemoteSettings
	if err := json.Unmarshal([]byte(`{"contextual_hints":{"undo":false}}`), &settings); err != nil {
		t.Fatal(err)
	}
	if settings.ContextualHints == nil || settings.ContextualHints.Undo == nil || *settings.ContextualHints.Undo {
		t.Fatalf("settings=%#v", settings.ContextualHints)
	}
}
