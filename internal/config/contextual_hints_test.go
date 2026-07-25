package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestContextualHintsDefaultAndLocalConfig(t *testing.T) {
	t.Setenv("GROK_HOME", t.TempDir())
	t.Setenv("GROK_CONTEXTUAL_HINTS", "")
	cfg, err := Load(filepath.Join(t.TempDir(), "missing.toml"))
	if err != nil || !cfg.UI.ContextualHints.Undo || !cfg.UI.ContextualHints.PlanMode ||
		!cfg.UI.ContextualHints.SendNow || !cfg.UI.ContextualHints.SmallScreen {
		t.Fatalf("default=%#v err=%v", cfg.UI.ContextualHints, err)
	}

	for _, test := range []struct {
		name, file, content string
	}{
		{name: "toml", file: "config.toml", content: "[ui.contextual_hints]\nundo = false\nplan_mode = false\nsend_now = false\nsmall_screen = false\n"},
		{name: "json", file: "config.json", content: `{"ui":{"contextual_hints":{"undo":false,"plan_mode":false,"send_now":false,"small_screen":false}}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), test.file)
			if err := os.WriteFile(path, []byte(test.content), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(path)
			if err != nil || cfg.UI.ContextualHints.Undo || cfg.UI.ContextualHints.PlanMode ||
				cfg.UI.ContextualHints.SendNow || cfg.UI.ContextualHints.SmallScreen {
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

func TestContextualPlanModeHintRemoteAndLocalPrecedence(t *testing.T) {
	cfg := Config{UI: UIConfig{ContextualHints: Hints{PlanMode: true}}}
	cfg.ApplyRemoteSettings(&RemoteSettings{ContextualHints: &RemoteHints{PlanMode: boolPointer(false)}})
	if cfg.UI.ContextualHints.PlanMode {
		t.Fatal("remote false was ignored")
	}
	cfg.ApplyRemoteSettings(&RemoteSettings{})
	if !cfg.UI.ContextualHints.PlanMode {
		t.Fatal("missing remote value did not restore the default")
	}

	for _, local := range []bool{false, true} {
		cfg = Config{
			UI:                             UIConfig{ContextualHints: Hints{PlanMode: local}},
			uiContextualPlanModeConfigured: true,
		}
		cfg.ApplyRemoteSettings(&RemoteSettings{ContextualHints: &RemoteHints{PlanMode: boolPointer(!local)}})
		if cfg.UI.ContextualHints.PlanMode != local {
			t.Fatalf("local=%v resolved=%v", local, cfg.UI.ContextualHints.PlanMode)
		}
	}
}

func TestContextualSendNowHintRemoteAndLocalPrecedence(t *testing.T) {
	cfg := Config{UI: UIConfig{ContextualHints: Hints{SendNow: true}}}
	cfg.ApplyRemoteSettings(&RemoteSettings{ContextualHints: &RemoteHints{SendNow: boolPointer(false)}})
	if cfg.UI.ContextualHints.SendNow {
		t.Fatal("remote false was ignored")
	}
	cfg.ApplyRemoteSettings(&RemoteSettings{})
	if !cfg.UI.ContextualHints.SendNow {
		t.Fatal("missing remote value did not restore the default")
	}

	for _, local := range []bool{false, true} {
		cfg = Config{
			UI:                            UIConfig{ContextualHints: Hints{SendNow: local}},
			uiContextualSendNowConfigured: true,
		}
		cfg.ApplyRemoteSettings(&RemoteSettings{ContextualHints: &RemoteHints{SendNow: boolPointer(!local)}})
		if cfg.UI.ContextualHints.SendNow != local {
			t.Fatalf("local=%v resolved=%v", local, cfg.UI.ContextualHints.SendNow)
		}
	}
}

func TestContextualSmallScreenHintRemoteAndLocalPrecedence(t *testing.T) {
	cfg := Config{UI: UIConfig{ContextualHints: Hints{SmallScreen: true}}}
	cfg.ApplyRemoteSettings(&RemoteSettings{ContextualHints: &RemoteHints{SmallScreen: boolPointer(false)}})
	if cfg.UI.ContextualHints.SmallScreen {
		t.Fatal("remote false was ignored")
	}
	cfg.ApplyRemoteSettings(&RemoteSettings{})
	if !cfg.UI.ContextualHints.SmallScreen {
		t.Fatal("missing remote value did not restore the default")
	}

	for _, local := range []bool{false, true} {
		cfg = Config{
			UI:                          UIConfig{ContextualHints: Hints{SmallScreen: local}},
			uiContextualSmallConfigured: true,
		}
		cfg.ApplyRemoteSettings(&RemoteSettings{ContextualHints: &RemoteHints{SmallScreen: boolPointer(!local)}})
		if cfg.UI.ContextualHints.SmallScreen != local {
			t.Fatalf("local=%v resolved=%v", local, cfg.UI.ContextualHints.SmallScreen)
		}
	}
}

func TestContextualHintsEnvironmentOverridesLocalAndRemote(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "off", true: "on"}[enabled], func(t *testing.T) {
			t.Setenv("GROK_CONTEXTUAL_HINTS", map[bool]string{false: "0", true: "1"}[enabled])
			path := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(path, []byte("[ui.contextual_hints]\nundo = true\nplan_mode = true\nsend_now = true\nsmall_screen = true\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			cfg.ApplyRemoteSettings(&RemoteSettings{ContextualHints: &RemoteHints{
				Undo: boolPointer(!enabled), PlanMode: boolPointer(!enabled),
				SendNow: boolPointer(!enabled), SmallScreen: boolPointer(!enabled),
			}})
			if cfg.UI.ContextualHints.Undo != enabled || cfg.UI.ContextualHints.PlanMode != enabled ||
				cfg.UI.ContextualHints.SendNow != enabled || cfg.UI.ContextualHints.SmallScreen != enabled {
				t.Fatalf("hints=%#v", cfg.UI.ContextualHints)
			}
		})
	}
}

func TestContextualHintsEnvironmentOverridesRequirements(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	t.Setenv("GROK_CONTEXTUAL_HINTS", "0")
	if err := os.WriteFile(
		filepath.Join(home, "requirements.toml"),
		[]byte("[ui.contextual_hints]\nundo = true\nplan_mode = true\nsend_now = true\nsmall_screen = true\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(filepath.Join(home, "missing.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.UI.ContextualHints.Undo || cfg.UI.ContextualHints.PlanMode ||
		cfg.UI.ContextualHints.SendNow || cfg.UI.ContextualHints.SmallScreen {
		t.Fatalf("requirements overrode environment: %#v", cfg.UI.ContextualHints)
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
			if err := UpdateContextualPlanModeHint(path, true); err != nil {
				t.Fatal(err)
			}
			if err := UpdateContextualSendNowHint(path, false); err != nil {
				t.Fatal(err)
			}
			if err := UpdateContextualSmallScreenHint(path, false); err != nil {
				t.Fatal(err)
			}
			root, err := readConfigMap(path)
			if err != nil {
				t.Fatal(err)
			}
			ui := root["ui"].(map[string]any)
			hints := ui["contextual_hints"].(map[string]any)
			if root["model_name"] != "test" || hints["plan_mode"] != true || hints["undo"] != false ||
				hints["send_now"] != false || hints["small_screen"] != false {
				data, _ := json.Marshal(root)
				t.Fatalf("config=%s", data)
			}
			if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o640 {
				t.Fatalf("mode=%v err=%v", info, err)
			}
		})
	}
}

func TestRemoteSettingsDecodesContextualHints(t *testing.T) {
	var settings RemoteSettings
	if err := json.Unmarshal([]byte(`{"contextual_hints":{"undo":false,"plan_mode":true,"send_now":true,"small_screen":false}}`), &settings); err != nil {
		t.Fatal(err)
	}
	if settings.ContextualHints == nil || settings.ContextualHints.Undo == nil || *settings.ContextualHints.Undo ||
		settings.ContextualHints.PlanMode == nil || !*settings.ContextualHints.PlanMode ||
		settings.ContextualHints.SendNow == nil || !*settings.ContextualHints.SendNow ||
		settings.ContextualHints.SmallScreen == nil || *settings.ContextualHints.SmallScreen {
		t.Fatalf("settings=%#v", settings.ContextualHints)
	}
}
