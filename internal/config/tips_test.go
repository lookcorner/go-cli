package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestTipsResolveLayersAndLocalOptOut(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	path := filepath.Join(home, "config.toml")
	files := map[string]string{
		"requirements.toml":   "[tips]\ntips = [\"requirements\"]\n",
		"managed_config.toml": "[tips]\ntips = [\"managed\"]\n",
		"config.toml":         "[tips]\ntips = [\"user\"]\n",
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(home, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ApplyRemoteSettings(&RemoteSettings{Tips: []string{"remote"}})
	if want := []string{"requirements", "remote", "user", "managed"}; !reflect.DeepEqual(cfg.Tips, want) {
		t.Fatalf("tips=%v want=%v", cfg.Tips, want)
	}

	if err := os.WriteFile(path, []byte("[cli]\nshow_tips = false\n[tips]\ntips = [\"user\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ApplyRemoteSettings(&RemoteSettings{Tips: []string{"remote"}})
	if cfg.ShowTips || len(cfg.Tips) != 0 {
		t.Fatalf("show=%v tips=%v", cfg.ShowTips, cfg.Tips)
	}
}

func TestTipsExcludeDefaultAndPersistence(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	path := filepath.Join(home, "config.json")
	if err := os.WriteFile(path, []byte(`{"tips":{"tips":["user"],"exclude_default":true}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ApplyRemoteSettings(&RemoteSettings{Tips: []string{"remote"}})
	if want := []string{"user"}; !reflect.DeepEqual(cfg.Tips, want) {
		t.Fatalf("tips=%v want=%v", cfg.Tips, want)
	}
	if err := UpdateShowTips(path, false); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(path)
	if err != nil || cfg.ShowTips {
		t.Fatalf("show=%v err=%v", cfg.ShowTips, err)
	}
}

func TestRemoteTipsDistinguishMissingAndEmpty(t *testing.T) {
	cfg := Config{ShowTips: true, Tips: []string{"existing"}}
	var missing RemoteSettings
	if err := json.Unmarshal([]byte(`{}`), &missing); err != nil {
		t.Fatal(err)
	}
	cfg.ApplyRemoteSettings(&missing)
	if want := []string{"existing"}; !reflect.DeepEqual(cfg.Tips, want) {
		t.Fatalf("missing tips=%v want=%v", cfg.Tips, want)
	}
	var empty RemoteSettings
	if err := json.Unmarshal([]byte(`{"tips":[]}`), &empty); err != nil {
		t.Fatal(err)
	}
	cfg.ApplyRemoteSettings(&empty)
	if len(cfg.Tips) != 0 {
		t.Fatalf("empty tips=%v", cfg.Tips)
	}
}
