package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDisplayRefreshDefaultsLocalAndEnvironment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	data := []byte("[ui.display_refresh]\nprobe_enabled = false\nauto_cadence_enabled = true\nfloor_ms = 4\nceiling_ms = 12\nmin_hz = 50\nmax_hz = 200\n")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil || cfg.UI.DisplayRefresh != (DisplayRefreshConfig{ProbeEnabled: false, AutoCadenceEnabled: true, FloorMS: 4, CeilingMS: 12, MinHz: 50, MaxHz: 200}) {
		t.Fatalf("config=%#v err=%v", cfg.UI.DisplayRefresh, err)
	}
	t.Setenv("GROK_DISPLAY_REFRESH_PROBE_ENABLED", "1")
	t.Setenv("GROK_DISPLAY_REFRESH_AUTO_CADENCE", "0")
	cfg, err = Load(path)
	if err != nil || !cfg.UI.DisplayRefresh.ProbeEnabled || cfg.UI.DisplayRefresh.AutoCadenceEnabled {
		t.Fatalf("environment config=%#v err=%v", cfg.UI.DisplayRefresh, err)
	}
}

func TestDisplayRefreshRemoteUsesUnconfiguredFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[ui.display_refresh]\nfloor_ms = 6\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	auto, floor, ceiling, minHz, maxHz := true, uint32(3), uint32(10), uint32(48), uint32(180)
	cfg.ApplyRemoteSettings(&RemoteSettings{DisplayRefresh: &RefreshSettings{
		AutoCadenceEnabled: &auto, FloorMS: &floor, CeilingMS: &ceiling, MinHz: &minHz, MaxHz: &maxHz,
	}})
	if !cfg.UI.DisplayRefresh.AutoCadenceEnabled || cfg.UI.DisplayRefresh.FloorMS != 6 || cfg.UI.DisplayRefresh.CeilingMS != 10 || cfg.UI.DisplayRefresh.MinHz != 48 || cfg.UI.DisplayRefresh.MaxHz != 180 {
		t.Fatalf("config=%#v", cfg.UI.DisplayRefresh)
	}
}

func TestDisplayRefreshBoundsNormalize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[ui.display_refresh]\nfloor_ms = 200\nceiling_ms = 0\nmin_hz = 200\nmax_hz = 50\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil || cfg.UI.DisplayRefresh.FloorMS != 8 || cfg.UI.DisplayRefresh.CeilingMS != 16 || cfg.UI.DisplayRefresh.MinHz != 55 || cfg.UI.DisplayRefresh.MaxHz != 165 {
		t.Fatalf("config=%#v err=%v", cfg.UI.DisplayRefresh, err)
	}
}

func TestDisplayRefreshHigherPrioritySingleBoundWins(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[ui.display_refresh]\nfloor_ms = 20\nmin_hz = 200\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil || cfg.UI.DisplayRefresh.FloorMS != 20 || cfg.UI.DisplayRefresh.CeilingMS != 20 || cfg.UI.DisplayRefresh.MinHz != 200 || cfg.UI.DisplayRefresh.MaxHz != 200 {
		t.Fatalf("config=%#v err=%v", cfg.UI.DisplayRefresh, err)
	}
	ceiling, maxHz := uint32(10), uint32(100)
	cfg.ApplyRemoteSettings(&RemoteSettings{DisplayRefresh: &RefreshSettings{CeilingMS: &ceiling, MaxHz: &maxHz}})
	if cfg.UI.DisplayRefresh.CeilingMS != 20 || cfg.UI.DisplayRefresh.MaxHz != 200 {
		t.Fatalf("remote overrode higher priority bounds: %#v", cfg.UI.DisplayRefresh)
	}
}

func TestUpdateDisplayRefreshAutoCadencePreservesConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"ui":{"theme":"grokday","display_refresh":{"floor_ms":7}},"models":{"default":"local"}}`), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := UpdateDisplayRefreshAutoCadence(path, true); err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	data, _ := os.ReadFile(path)
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatal(err)
	}
	ui := root["ui"].(map[string]any)
	display := ui["display_refresh"].(map[string]any)
	if ui["theme"] != "grokday" || display["floor_ms"] != float64(7) || display["auto_cadence_enabled"] != true || root["models"] == nil {
		t.Fatalf("config=%s", data)
	}
}

func TestRefreshSettingsJSONIgnoresInvalidFields(t *testing.T) {
	var settings RemoteSettings
	if err := json.Unmarshal([]byte(`{"display_refresh":{"probe_enabled":true,"auto_cadence_enabled":"bad","floor_ms":7,"ceiling_ms":false,"min_hz":50,"future":42}}`), &settings); err != nil {
		t.Fatal(err)
	}
	if settings.DisplayRefresh == nil || settings.DisplayRefresh.ProbeEnabled == nil || !*settings.DisplayRefresh.ProbeEnabled ||
		settings.DisplayRefresh.AutoCadenceEnabled != nil || settings.DisplayRefresh.FloorMS == nil || *settings.DisplayRefresh.FloorMS != 7 ||
		settings.DisplayRefresh.CeilingMS != nil || settings.DisplayRefresh.MinHz == nil || *settings.DisplayRefresh.MinHz != 50 {
		t.Fatalf("settings=%#v", settings.DisplayRefresh)
	}
}
