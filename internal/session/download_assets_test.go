package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestListAndResolveDownloadAssets(t *testing.T) {
	dir := t.TempDir()
	sessionPath := filepath.Join(dir, "s.jsonl")
	downloads := filepath.Join(dir, "artifacts", "s", "downloads")
	if err := os.MkdirAll(downloads, 0o700); err != nil {
		t.Fatal(err)
	}
	older := filepath.Join(downloads, "1.pdf")
	newer := filepath.Join(downloads, "2.pdf")
	if err := os.WriteFile(older, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	if err := os.WriteFile(newer, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(downloads, ".hidden"), []byte("skip"), 0o600)
	_ = os.MkdirAll(filepath.Join(dir, "downloads"), 0o700)
	_ = os.WriteFile(filepath.Join(dir, "downloads", "sibling.pdf"), []byte("nope"), 0o600)

	assets, err := ListDownloadAssets(sessionPath)
	if err != nil || len(assets) != 2 {
		t.Fatalf("assets=%#v err=%v", assets, err)
	}
	if assets[0].Name != "2.pdf" || assets[1].Name != "1.pdf" {
		t.Fatalf("order=%v %v", assets[0].Name, assets[1].Name)
	}
	if assets[0].URI != "downloads/2.pdf" || assets[0].Path != newer {
		t.Fatalf("uri/path=%q %q", assets[0].URI, assets[0].Path)
	}

	latest, ok, err := LatestDownloadAsset(sessionPath)
	if err != nil || !ok || latest.Name != "2.pdf" {
		t.Fatalf("latest=%#v ok=%v err=%v", latest, ok, err)
	}

	path, err := ResolveDownloadAsset(sessionPath, "1.pdf")
	if err != nil || path != older {
		t.Fatalf("resolve basename path=%q err=%v", path, err)
	}
	path, err = ResolveDownloadAsset(sessionPath, "downloads/2.pdf")
	if err != nil || path != newer {
		t.Fatalf("resolve uri path=%q err=%v", path, err)
	}
	if _, err := ResolveDownloadAsset(sessionPath, "missing.pdf"); err == nil {
		t.Fatal("missing accepted")
	}
	if _, err := ResolveDownloadAsset(sessionPath, "../escape.pdf"); err == nil {
		t.Fatal("escape accepted")
	}
}

func TestListDownloadAssetsEmptyAndSymlinkReject(t *testing.T) {
	dir := t.TempDir()
	sessionPath := filepath.Join(dir, "s.jsonl")
	assets, err := ListDownloadAssets(sessionPath)
	if err != nil || len(assets) != 0 {
		t.Fatalf("empty=%#v err=%v", assets, err)
	}

	outside := t.TempDir()
	downloads := filepath.Join(dir, "artifacts", "s", "downloads")
	if err := os.MkdirAll(filepath.Dir(downloads), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, downloads); err != nil {
		t.Skip("symlink unsupported:", err)
	}
	if _, err := ListDownloadAssets(sessionPath); err == nil {
		t.Fatal("symlinked downloads/ accepted")
	}
}
