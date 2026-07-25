package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpdateVoiceSTTLanguageCanonicalizesAndPreservesConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[models]\ndefault = \"local\"\n\n[ui]\ntheme = \"grokday\"\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := UpdateVoiceSTTLanguage(path, "tl-PH"); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil || cfg.UI.VoiceSTTLanguage != "fil" || cfg.UI.Theme != "grokday" || cfg.DefaultModelID != "local" {
		t.Fatalf("config=%#v err=%v", cfg, err)
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), `voice_stt_language = 'fil'`) {
		t.Fatalf("config=%q err=%v", data, err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != wantConfigPerm(0o640) {
		t.Fatalf("mode info=%v err=%v", info, err)
	}
	if err := UpdateVoiceSTTLanguage(path, "unknown"); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(path)
	if err != nil || cfg.UI.VoiceSTTLanguage != "en" {
		t.Fatalf("fallback language=%q err=%v", cfg.UI.VoiceSTTLanguage, err)
	}
}
