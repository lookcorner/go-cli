package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lookcorner/go-cli/internal/agent"
)

func TestResolvePlayVideoPathUsesLatestSessionClip(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	sessionPath := filepath.Join(root, "clip-session.jsonl")
	if err := os.WriteFile(sessionPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	videos := filepath.Join(root, "artifacts", "clip-session", "videos")
	if err := os.MkdirAll(videos, 0o700); err != nil {
		t.Fatal(err)
	}
	clip := filepath.Join(videos, "3.mp4")
	if err := os.WriteFile(clip, []byte("mp4"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := &model{runner: &agent.Runner{SessionPath: sessionPath}, workspace: root}
	resolved, err := m.resolvePlayVideoPath("")
	if err != nil || resolved != clip {
		t.Fatalf("resolved=%q err=%v", resolved, err)
	}
	resolved, err = m.resolvePlayVideoPath("videos/3.mp4")
	if err != nil || resolved != clip {
		t.Fatalf("relative=%q err=%v", resolved, err)
	}
}

func TestListSessionVideos(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	sessionPath := filepath.Join(root, "list-session.jsonl")
	if err := os.WriteFile(sessionPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	videos := filepath.Join(root, "artifacts", "list-session", "videos")
	if err := os.MkdirAll(videos, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(videos, "1.mp4"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := &model{runner: &agent.Runner{SessionPath: sessionPath}}
	text := m.listSessionVideos()
	if !strings.Contains(text, "videos/1.mp4") || !strings.Contains(text, "/play-video") {
		t.Fatalf("list=%q", text)
	}
	empty := &model{runner: &agent.Runner{SessionPath: filepath.Join(root, "missing.jsonl")}}
	_ = os.WriteFile(empty.runner.SessionPath, []byte("{}\n"), 0o600)
	if !strings.Contains(empty.listSessionVideos(), "No session videos") {
		t.Fatalf("empty=%q", empty.listSessionVideos())
	}
}
