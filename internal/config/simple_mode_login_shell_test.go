package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSimpleModeDefaultsAndVimMirror(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[ui]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil || !cfg.UI.SimpleMode || cfg.UI.VimMode {
		t.Fatalf("defaults simple=%v vim=%v err=%v", cfg.UI.SimpleMode, cfg.UI.VimMode, err)
	}
	offPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(offPath, []byte("[ui]\nsimple_mode = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(offPath)
	if err != nil || cfg.UI.SimpleMode || !cfg.UI.VimMode {
		t.Fatalf("simple_mode=false should mirror vim: simple=%v vim=%v err=%v", cfg.UI.SimpleMode, cfg.UI.VimMode, err)
	}
	bothPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(bothPath, []byte("[ui]\nsimple_mode = false\nvim_mode = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(bothPath)
	if err != nil || cfg.UI.SimpleMode || cfg.UI.VimMode {
		t.Fatalf("explicit vim_mode wins: simple=%v vim=%v err=%v", cfg.UI.SimpleMode, cfg.UI.VimMode, err)
	}
	if err := UpdateSimpleMode(path, false); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(path)
	if err != nil || cfg.UI.SimpleMode {
		t.Fatalf("updated simple=%v err=%v", cfg.UI.SimpleMode, err)
	}
}

func TestLoginShellCaptureConfigAndEnv(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[toolset.bash]\nlogin_shell_capture = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil || cfg.Toolset.Bash.LoginShellCapture {
		t.Fatalf("toml off: %#v err=%v", cfg.Toolset.Bash, err)
	}
	t.Setenv("GROK_LOGIN_ENV", "true")
	cfg, err = Load(path)
	if err != nil || !cfg.Toolset.Bash.LoginShellCapture {
		t.Fatalf("env should override: %#v err=%v", cfg.Toolset.Bash, err)
	}
	empty := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(empty, []byte("[toolset.bash]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GROK_LOGIN_ENV", "")
	cfg, err = Load(empty)
	if err != nil || !cfg.Toolset.Bash.LoginShellCapture {
		t.Fatalf("default on: %#v err=%v", cfg.Toolset.Bash, err)
	}
}

func TestLoginShellCaptureRequirementsAndRemotePrecedence(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	path := filepath.Join(home, "config.toml")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	off := false
	cfg.ApplyRemoteSettings(&RemoteSettings{LoginShellCapture: &off})
	if cfg.Toolset.Bash.LoginShellCapture {
		t.Fatal("remote false did not disable login shell capture")
	}
	cfg.ApplyRemoteSettings(&RemoteSettings{})
	if !cfg.Toolset.Bash.LoginShellCapture {
		t.Fatal("missing remote value did not restore the default")
	}

	t.Setenv("GROK_LOGIN_ENV", "true")
	if err := os.WriteFile(filepath.Join(home, "requirements.toml"), []byte("[toolset.bash]\nlogin_shell_capture = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(path)
	if err != nil || cfg.Toolset.Bash.LoginShellCapture {
		t.Fatalf("requirements should override environment: %#v err=%v", cfg.Toolset.Bash, err)
	}
	on := true
	cfg.ApplyRemoteSettings(&RemoteSettings{LoginShellCapture: &on})
	if cfg.Toolset.Bash.LoginShellCapture {
		t.Fatal("remote value overrode requirements")
	}
}
