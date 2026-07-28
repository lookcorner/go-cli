package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestListAndResolveWebFetchAssets(t *testing.T) {
	dir := t.TempDir()
	sessionPath := filepath.Join(dir, "s.jsonl")
	root := filepath.Join(dir, "artifacts", "s", "web_fetch")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	older := filepath.Join(root, "1.md")
	newer := filepath.Join(root, "2.txt")
	if err := os.WriteFile(older, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	if err := os.WriteFile(newer, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(root, ".hidden"), []byte("skip"), 0o600)

	assets, err := ListWebFetchAssets(sessionPath)
	if err != nil || len(assets) != 2 {
		t.Fatalf("assets=%#v err=%v", assets, err)
	}
	if assets[0].Name != "2.txt" || assets[0].URI != "web_fetch/2.txt" {
		t.Fatalf("newest=%#v", assets[0])
	}
	latest, ok, err := LatestWebFetchAsset(sessionPath)
	if err != nil || !ok || latest.Path != newer {
		t.Fatalf("latest=%#v ok=%v err=%v", latest, ok, err)
	}
	path, err := ResolveWebFetchAsset(sessionPath, "1.md")
	if err != nil || path != older {
		t.Fatalf("basename path=%q err=%v", path, err)
	}
	path, err = ResolveWebFetchAsset(sessionPath, "web_fetch/2.txt")
	if err != nil || path != newer {
		t.Fatalf("uri path=%q err=%v", path, err)
	}
	if _, err := ResolveWebFetchAsset(sessionPath, "../escape.md"); err == nil {
		t.Fatal("escape accepted")
	}
}

func TestListWebFetchAssetsEmptyAndSymlinkReject(t *testing.T) {
	dir := t.TempDir()
	sessionPath := filepath.Join(dir, "s.jsonl")
	assets, err := ListWebFetchAssets(sessionPath)
	if err != nil || len(assets) != 0 {
		t.Fatalf("empty=%#v err=%v", assets, err)
	}
	outside := t.TempDir()
	root := filepath.Join(dir, "artifacts", "s", "web_fetch")
	if err := os.MkdirAll(filepath.Dir(root), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, root); err != nil {
		t.Skip("symlink unsupported:", err)
	}
	if _, err := ListWebFetchAssets(sessionPath); err == nil {
		t.Fatal("symlinked web_fetch/ accepted")
	}
}
