package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRespectManualFoldsPagerConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	path, err := PagerPath()
	if err != nil || path != filepath.Join(home, "pager.toml") {
		t.Fatalf("path=%q err=%v", path, err)
	}
	settings, err := LoadPagerScroll(path)
	if err != nil || !settings.AnchorOnFold || settings.FollowIndicator != "center" || !settings.FollowByOverscroll || settings.RespectManualFolds {
		t.Fatalf("defaults=%#v err=%v", settings, err)
	}
	if err := os.WriteFile(path, []byte("[scrollback.scroll]\nanchor_on_fold = false\nfollow_indicator = \"none\"\nfollow_by_overscroll = false\n\n[other]\nvalue = 1\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := UpdateRespectManualFolds(path, true); err != nil {
		t.Fatal(err)
	}
	settings, err = LoadPagerScroll(path)
	if err != nil || settings.AnchorOnFold || settings.FollowIndicator != "none" || settings.FollowByOverscroll || !settings.RespectManualFolds {
		t.Fatalf("settings=%#v err=%v", settings, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "value = 1") || !strings.Contains(text, "respect_manual_folds = true") {
		t.Fatalf("pager config=%q", text)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("mode=%v", info.Mode().Perm())
	}
	if err := os.WriteFile(path, []byte("[scrollback.scroll]\nfollow_indicator = \"side\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPagerScroll(path); err == nil || !strings.Contains(err.Error(), "follow_indicator") {
		t.Fatalf("invalid indicator error=%v", err)
	}
}
