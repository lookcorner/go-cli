package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDreamPreparationGatesAndBoundsInput(t *testing.T) {
	store, err := Open(t.TempDir(), t.TempDir(), "current")
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-10 * time.Minute)
	for _, name := range []string{"a.md", "b.md", "c.md"} {
		path := filepath.Join(store.sessionsDir, name)
		if err := os.WriteFile(path, []byte("## "+name+"\n\nreusable decision"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(store.workspaceDir, "MEMORY.md"), []byte("## Existing\n\nKeep prior context."), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig().Dream
	cfg.MinSessions = 4
	if _, skipped, err := store.PrepareDream(cfg, false); err != nil || skipped.Outcome != "too_few_sessions" || skipped.Eligible != 3 {
		t.Fatalf("skipped=%#v err=%v", skipped, err)
	}
	input, skipped, err := store.PrepareDream(cfg, true)
	if err != nil || skipped.Outcome != "" || input.Eligible != 3 || len(input.paths) != 3 || !strings.Contains(input.Content, "Existing Memory") || !strings.Contains(input.Content, "Session: a") {
		t.Fatalf("input=%#v skipped=%#v err=%v", input, skipped, err)
	}
}

func TestDreamCommitWritesThenCleansOnlyOldSessions(t *testing.T) {
	store, err := Open(t.TempDir(), t.TempDir(), "current")
	if err != nil {
		t.Fatal(err)
	}
	oldPath, recentPath := filepath.Join(store.sessionsDir, "old.md"), filepath.Join(store.sessionsDir, "recent.md")
	for _, path := range []string{oldPath, recentPath} {
		if err := os.WriteFile(path, []byte("memory"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-10 * time.Minute)
	if err := os.Chtimes(oldPath, old, old); err != nil {
		t.Fatal(err)
	}
	input := DreamInput{Eligible: 2, paths: []string{oldPath, recentPath}}
	result, err := store.CommitDream("## Architecture\n\nKeep clear boundaries.", input, 3600)
	if err != nil || result.Outcome != "written" || result.Cleaned != 1 || result.Eligible != 2 || result.Path == "" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	data, err := os.ReadFile(result.Path)
	if err != nil || string(data) != "## Architecture\n\nKeep clear boundaries." {
		t.Fatalf("data=%q err=%v", data, err)
	}
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatalf("old session survived: %v", err)
	}
	if _, err := os.Stat(recentPath); err != nil {
		t.Fatalf("recent session removed: %v", err)
	}
}

func TestDreamRejectsBadOutputAndRollsBackWriteFailure(t *testing.T) {
	store, err := Open(t.TempDir(), t.TempDir(), "current")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store.sessionsDir, "old.md")
	if err := os.WriteFile(path, []byte("memory"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-10 * time.Minute)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	input := DreamInput{Eligible: 1, paths: []string{path}}
	result, err := store.CommitDream("plain text", input, 0)
	if err != nil || result.Outcome != "nothing_to_consolidate" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("bad output deleted session")
	}

	lockPath := filepath.Join(store.workspaceDir, ".dream-lock")
	prior := time.Now().Add(-2 * time.Hour)
	if err := os.WriteFile(lockPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(lockPath, prior, prior); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(target, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(store.workspaceDir, "MEMORY.md")); err != nil {
		t.Fatal(err)
	}
	if result, err = store.CommitDream("## Valid\n\ncontent", input, 0); err == nil || result.Outcome != "failed" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	info, err := os.Stat(lockPath)
	if err != nil || info.ModTime().Sub(prior) > 2*time.Second {
		t.Fatalf("lock mtime=%v prior=%v err=%v", info.ModTime(), prior, err)
	}
	if data, _ := os.ReadFile(target); string(data) != "outside" {
		t.Fatal("symlink target changed")
	}
}
