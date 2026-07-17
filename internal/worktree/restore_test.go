package worktree

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRestoreCommitStashesDirtyStateAndChecksOutHistoricalHead(t *testing.T) {
	root := newRepo(t)
	first := strings.TrimSpace(runGitOutput(t, root, "rev-parse", "HEAD"))
	if err := os.WriteFile(filepath.Join(root, "second.txt"), []byte("second\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "second.txt")
	runGit(t, root, "commit", "-qm", "second")
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("dirty\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "untracked.txt"), []byte("new\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	outcome := RestoreCommit(context.Background(), root, first, "restore-session")
	if !outcome.CheckedOut || outcome.StashRef == "" || outcome.StashSkippedReason != "" {
		t.Fatalf("restore outcome: %#v", outcome)
	}
	if head := strings.TrimSpace(runGitOutput(t, root, "rev-parse", "HEAD")); head != first {
		t.Fatalf("HEAD = %s, want %s", head, first)
	}
	if _, err := os.Stat(filepath.Join(root, "second.txt")); !os.IsNotExist(err) {
		t.Fatalf("historical checkout retained second commit file: %v", err)
	}
	if status := strings.TrimSpace(runGitOutput(t, root, "status", "--porcelain")); status != "" {
		t.Fatalf("restored tree is dirty: %q", status)
	}
	if list := runGitOutput(t, root, "stash", "list"); !strings.Contains(list, "grok: pre-restore-code restore-session") {
		t.Fatalf("stash label missing: %q", list)
	}
	restored, summary, degree := RestoreSummary(first, outcome)
	if !restored || degree != "head_only" || !strings.Contains(summary, outcome.StashRef) {
		t.Fatalf("restore summary: restored=%v degree=%q summary=%q", restored, degree, summary)
	}
}
