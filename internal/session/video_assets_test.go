package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestListAndResolveVideoAssets(t *testing.T) {
	dir := t.TempDir()
	sessionPath := filepath.Join(dir, "s.jsonl")
	videos := filepath.Join(dir, "videos")
	if err := os.MkdirAll(videos, 0o700); err != nil {
		t.Fatal(err)
	}
	older := filepath.Join(videos, "1.mp4")
	newer := filepath.Join(videos, "2.mp4")
	if err := os.WriteFile(older, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	if err := os.WriteFile(newer, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(videos, "notes.txt"), []byte("skip"), 0o600)

	assets, err := ListVideoAssets(sessionPath)
	if err != nil || len(assets) != 2 {
		t.Fatalf("assets=%#v err=%v", assets, err)
	}
	if assets[0].Name != "2.mp4" || assets[1].Name != "1.mp4" {
		t.Fatalf("order=%v %v", assets[0].Name, assets[1].Name)
	}
	if assets[0].URI != "videos/2.mp4" {
		t.Fatalf("uri=%q", assets[0].URI)
	}

	latest, ok, err := LatestVideoAsset(sessionPath)
	if err != nil || !ok || latest.Name != "2.mp4" {
		t.Fatalf("latest=%#v ok=%v err=%v", latest, ok, err)
	}

	path, err := ResolveVideoAsset(sessionPath, "1.mp4")
	if err != nil || path != older {
		t.Fatalf("resolve basename path=%q err=%v", path, err)
	}
	path, err = ResolveVideoAsset(sessionPath, "videos/2.mp4")
	if err != nil || path != newer {
		t.Fatalf("resolve uri path=%q err=%v", path, err)
	}
	if _, err := ResolveVideoAsset(sessionPath, "missing.mp4"); err == nil {
		t.Fatal("missing accepted")
	}
	if _, err := ResolveVideoAsset(sessionPath, "../escape.mp4"); err == nil {
		t.Fatal("escape accepted")
	}
}

func TestListVideoAssetsEmptyAndSymlinkReject(t *testing.T) {
	dir := t.TempDir()
	sessionPath := filepath.Join(dir, "s.jsonl")
	assets, err := ListVideoAssets(sessionPath)
	if err != nil || len(assets) != 0 {
		t.Fatalf("empty=%#v err=%v", assets, err)
	}

	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "videos")); err != nil {
		t.Skip("symlink unsupported:", err)
	}
	if _, err := ListVideoAssets(sessionPath); err == nil {
		t.Fatal("symlinked videos/ accepted")
	}
}
