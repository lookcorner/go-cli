package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReleaseChannelDefaultsAndPersistsWithoutLosingConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if channel, err := ReleaseChannel(path); err != nil || channel != "stable" {
		t.Fatalf("default channel=%q err=%v", channel, err)
	}
	if err := os.WriteFile(path, []byte("model_name = \"grok-4\"\n[cli]\nauto_update = false\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := UpdateReleaseChannel(path, "ALPHA"); err != nil {
		t.Fatal(err)
	}
	channel, err := ReleaseChannel(path)
	if err != nil || channel != "alpha" {
		t.Fatalf("channel=%q err=%v", channel, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{`model_name = 'grok-4'`, "auto_update = false", "channel = 'alpha'"} {
		if !strings.Contains(text, want) {
			t.Fatalf("config missing %q:\n%s", want, text)
		}
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != wantConfigPerm(0o640) {
		t.Fatalf("mode=%v err=%v", info.Mode().Perm(), err)
	}
}

func TestReleaseChannelRejectsInvalidValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[cli]\nchannel = \"nightly\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReleaseChannel(path); err == nil {
		t.Fatal("invalid stored channel was accepted")
	}
	if err := UpdateReleaseChannel(path, "nightly"); err == nil {
		t.Fatal("invalid update channel was accepted")
	}
}
