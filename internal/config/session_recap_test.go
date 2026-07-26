package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSessionRecapDefaultsAndOverrides(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "missing.toml"))
	if err != nil || !cfg.SessionRecapEnabled || !cfg.UI.Notifications.SessionRecap ||
		cfg.UI.Notifications.RecapThresholdSecs != 30 {
		t.Fatalf("defaults feature=%v notifications=%+v err=%v", cfg.SessionRecapEnabled, cfg.UI.Notifications, err)
	}

	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(
		"[features]\nsession_recap = false\n\n[ui.notifications]\nsession_recap = false\nsession_recap_threshold_secs = 90\n",
	), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(path)
	if err != nil || cfg.SessionRecapEnabled || cfg.UI.Notifications.SessionRecap ||
		cfg.UI.Notifications.RecapThresholdSecs != 90 {
		t.Fatalf("configured feature=%v notifications=%+v err=%v", cfg.SessionRecapEnabled, cfg.UI.Notifications, err)
	}

	t.Setenv("GROK_SESSION_RECAP", "1")
	cfg, err = Load(path)
	if err != nil || !cfg.SessionRecapEnabled {
		t.Fatalf("environment override=%v err=%v", cfg.SessionRecapEnabled, err)
	}
}

func TestSessionRecapRemoteAndLocalPrecedence(t *testing.T) {
	cfg := Config{SessionRecapEnabled: true}
	cfg.ApplyRemoteSettings(&RemoteSettings{SessionRecap: boolPointer(false)})
	if cfg.SessionRecapEnabled {
		t.Fatal("remote false was ignored")
	}
	cfg.ApplyRemoteSettings(&RemoteSettings{})
	if !cfg.SessionRecapEnabled {
		t.Fatal("missing remote value did not restore the default")
	}

	cfg = Config{SessionRecapEnabled: false, sessionRecapConfigured: true}
	cfg.ApplyRemoteSettings(&RemoteSettings{SessionRecap: boolPointer(true)})
	if cfg.SessionRecapEnabled {
		t.Fatal("remote setting overrode local feature choice")
	}
}
