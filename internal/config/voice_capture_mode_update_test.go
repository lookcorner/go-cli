package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVoiceCaptureModeDefaultsAndCanonicalizes(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "missing.toml"))
	if err != nil || cfg.UI.VoiceCaptureMode != "hold" {
		t.Fatalf("default mode=%q", cfg.UI.VoiceCaptureMode)
	}

	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[ui]\nvoice_capture_mode = \" TOGGLE \"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(path)
	if err != nil || cfg.UI.VoiceCaptureMode != "toggle" {
		t.Fatalf("mode=%q err=%v", cfg.UI.VoiceCaptureMode, err)
	}

	if err := os.WriteFile(path, []byte("[ui]\nvoice_capture_mode = \"unknown\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(path)
	if err != nil || cfg.UI.VoiceCaptureMode != "hold" {
		t.Fatalf("fallback mode=%q err=%v", cfg.UI.VoiceCaptureMode, err)
	}
}

func TestUpdateVoiceCaptureModePreservesConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[models]\ndefault = \"local\"\n\n[ui]\ntheme = \"grokday\"\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := UpdateVoiceCaptureMode(path, "toggle"); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil || cfg.UI.VoiceCaptureMode != "toggle" || cfg.UI.Theme != "grokday" || cfg.DefaultModelID != "local" {
		t.Fatalf("config=%#v err=%v", cfg, err)
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), `voice_capture_mode = 'toggle'`) {
		t.Fatalf("config=%q err=%v", data, err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("mode info=%v err=%v", info, err)
	}
}
