package worktree

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestGitStatusStageUnstageAndDiscard(t *testing.T) {
	ctx := context.Background()
	root := newRepo(t)
	if err := os.WriteFile(filepath.Join(root, "both.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "both.txt")
	runGit(t, root, "commit", "-qm", "add status fixture")
	if err := os.WriteFile(filepath.Join(root, "both.txt"), []byte("staged\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "both.txt")
	if err := os.WriteFile(filepath.Join(root, "both.txt"), []byte("working\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "new.txt"), []byte("new\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	status, err := Status(ctx, root, true, true)
	if err != nil {
		t.Fatal(err)
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if status.Root != resolvedRoot || status.MainRoot != resolvedRoot || status.IsWorktree || status.Commit == "" {
		t.Fatalf("unexpected repository metadata: %#v", status)
	}
	if len(status.Staged) != 1 || status.Staged[0].Path != "both.txt" || !status.Staged[0].Staged || status.Staged[0].Additions != 1 || status.Staged[0].Deletions != 1 {
		t.Fatalf("unexpected staged status: %#v", status.Staged)
	}
	if len(status.Unstaged) != 2 || status.Unstaged[0].Path != "both.txt" || status.Unstaged[0].Staged || status.Unstaged[1].Path != "new.txt" || status.Unstaged[1].Type != "untracked" {
		t.Fatalf("unexpected unstaged status: %#v", status.Unstaged)
	}
	if _, err := Stage(ctx, root, []string{"new.txt"}); err != nil {
		t.Fatal(err)
	}
	if err := Unstage(ctx, root, []string{"new.txt"}); err != nil {
		t.Fatal(err)
	}
	status, err = Status(ctx, root, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Unstaged) != 1 || status.Unstaged[0].Path != "both.txt" {
		t.Fatalf("includeUntracked=false status: %#v", status.Unstaged)
	}
	if err := Discard(ctx, root, nil, "both", true); err != nil {
		t.Fatal(err)
	}
	status, err = Status(ctx, root, true, false)
	if err != nil || len(status.Staged) != 0 || len(status.Unstaged) != 0 {
		t.Fatalf("discard did not clean repository: %#v err=%v", status, err)
	}
	data, err := os.ReadFile(filepath.Join(root, "both.txt"))
	if err != nil || string(data) != "base\n" {
		t.Fatalf("discarded file=%q err=%v", data, err)
	}
	if _, err := os.Stat(filepath.Join(root, "new.txt")); !os.IsNotExist(err) {
		t.Fatalf("discard kept untracked file: %v", err)
	}
}
