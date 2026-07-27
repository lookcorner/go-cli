package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestListVideoAssetsNewestFirst(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	sessionPath := filepath.Join(root, "session-1.jsonl")
	if err := os.WriteFile(sessionPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	videos, err := ArtifactDir(sessionPath)
	if err != nil {
		t.Fatal(err)
	}
	videos = filepath.Join(videos, "videos")
	if err := os.MkdirAll(videos, 0o700); err != nil {
		t.Fatal(err)
	}
	older := filepath.Join(videos, "1.mp4")
	newer := filepath.Join(videos, "2.mp4")
	if err := os.WriteFile(older, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newer, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.Chtimes(older, time.Now().Add(-time.Hour), time.Now().Add(-time.Hour))
	_ = os.Chtimes(newer, time.Now(), time.Now())
	if err := os.WriteFile(filepath.Join(videos, "notes.txt"), []byte("skip"), 0o600); err != nil {
		t.Fatal(err)
	}

	assets, err := ListVideoAssets(sessionPath)
	if err != nil || len(assets) != 2 {
		t.Fatalf("assets=%#v err=%v", assets, err)
	}
	if assets[0].Relative != "videos/2.mp4" || assets[1].Relative != "videos/1.mp4" {
		t.Fatalf("order=%#v", assets)
	}
	latest, ok, err := LatestVideoAsset(sessionPath)
	if err != nil || !ok || latest.Relative != "videos/2.mp4" {
		t.Fatalf("latest=%#v ok=%v err=%v", latest, ok, err)
	}
}

func TestResolveVideoPathSessionRelative(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	sessionPath := filepath.Join(root, "abc123.jsonl")
	if err := os.WriteFile(sessionPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	artifact, err := ArtifactDir(sessionPath)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(artifact, "videos", "1.mp4")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("clip"), 0o600); err != nil {
		t.Fatal(err)
	}
	resolved, err := ResolveVideoPath(sessionPath, "videos/1.mp4")
	if err != nil || resolved != path {
		t.Fatalf("resolved=%q err=%v", resolved, err)
	}
	resolved, err = ResolveVideoPath(sessionPath, "1.mp4")
	if err != nil || resolved != path {
		t.Fatalf("basename resolved=%q err=%v", resolved, err)
	}
	abs, err := ResolveVideoPath(sessionPath, path)
	if err != nil || abs != path {
		t.Fatalf("abs=%q err=%v", abs, err)
	}
	if _, err := ResolveVideoPath(sessionPath, "../secrets.mp4"); err == nil {
		t.Fatal("expected reject traversal")
	}
	if _, err := ResolveVideoPath(sessionPath, "images/1.mp4"); err == nil {
		t.Fatal("expected reject non-videos dir")
	}
}

func TestListVideoAssetsEmpty(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	sessionPath := filepath.Join(root, "empty-session.jsonl")
	if err := os.WriteFile(sessionPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	assets, err := ListVideoAssets(sessionPath)
	if err != nil || assets != nil {
		t.Fatalf("assets=%#v err=%v", assets, err)
	}
	if _, ok, err := LatestVideoAsset(sessionPath); err != nil || ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
}
