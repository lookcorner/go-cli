package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestListAndResolveImageAssets(t *testing.T) {
	dir := t.TempDir()
	sessionPath := filepath.Join(dir, "s.jsonl")
	images := filepath.Join(dir, "artifacts", "s", "images")
	if err := os.MkdirAll(images, 0o700); err != nil {
		t.Fatal(err)
	}
	older := filepath.Join(images, "1.png")
	newer := filepath.Join(images, "2.jpg")
	if err := os.WriteFile(older, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	if err := os.WriteFile(newer, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(images, "notes.txt"), []byte("skip"), 0o600)
	_ = os.MkdirAll(filepath.Join(dir, "images"), 0o700)
	_ = os.WriteFile(filepath.Join(dir, "images", "sibling.png"), []byte("nope"), 0o600)

	assets, err := ListImageAssets(sessionPath)
	if err != nil || len(assets) != 2 {
		t.Fatalf("assets=%#v err=%v", assets, err)
	}
	if assets[0].Name != "2.jpg" || assets[1].Name != "1.png" {
		t.Fatalf("order=%v %v", assets[0].Name, assets[1].Name)
	}
	if assets[0].URI != "images/2.jpg" || assets[0].Path != newer {
		t.Fatalf("uri/path=%q %q", assets[0].URI, assets[0].Path)
	}

	latest, ok, err := LatestImageAsset(sessionPath)
	if err != nil || !ok || latest.Name != "2.jpg" {
		t.Fatalf("latest=%#v ok=%v err=%v", latest, ok, err)
	}

	path, err := ResolveImageAsset(sessionPath, "1.png")
	if err != nil || path != older {
		t.Fatalf("resolve basename path=%q err=%v", path, err)
	}
	path, err = ResolveImageAsset(sessionPath, "images/2.jpg")
	if err != nil || path != newer {
		t.Fatalf("resolve uri path=%q err=%v", path, err)
	}
	if _, err := ResolveImageAsset(sessionPath, "missing.png"); err == nil {
		t.Fatal("missing accepted")
	}
	if _, err := ResolveImageAsset(sessionPath, "../escape.png"); err == nil {
		t.Fatal("escape accepted")
	}
}

func TestListImageAssetsEmptyAndSymlinkReject(t *testing.T) {
	dir := t.TempDir()
	sessionPath := filepath.Join(dir, "s.jsonl")
	assets, err := ListImageAssets(sessionPath)
	if err != nil || len(assets) != 0 {
		t.Fatalf("empty=%#v err=%v", assets, err)
	}

	outside := t.TempDir()
	images := filepath.Join(dir, "artifacts", "s", "images")
	if err := os.MkdirAll(filepath.Dir(images), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, images); err != nil {
		t.Skip("symlink unsupported:", err)
	}
	if _, err := ListImageAssets(sessionPath); err == nil {
		t.Fatal("symlinked images/ accepted")
	}
}
