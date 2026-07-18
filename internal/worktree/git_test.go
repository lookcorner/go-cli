package worktree

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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

func TestGitInfoBranchesCheckoutStashAndCommit(t *testing.T) {
	ctx := context.Background()
	root := newRepo(t)
	initial := strings.TrimSpace(runGitOutput(t, root, "rev-parse", "HEAD"))
	current := strings.TrimSpace(runGitOutput(t, root, "symbolic-ref", "--short", "HEAD"))
	runGit(t, root, "branch", "feature/topic")
	runGit(t, root, "remote", "add", "origin", "https://example.invalid/repo.git")
	info, err := Info(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if info.CurrentBranch != current || info.VCSKind != "git" || len(info.Remotes) != 1 || info.Remotes[0] != "https://example.invalid/repo.git" {
		t.Fatalf("unexpected git info: %#v", info)
	}
	branches, err := Branches(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if branches.CurrentBranch != current || len(branches.Branches) != 2 {
		t.Fatalf("unexpected branches: %#v", branches)
	}
	for _, branch := range branches.Branches {
		if branch.Name == "feature/topic" && branch.Remote {
			t.Fatalf("local slash branch was marked remote: %#v", branch)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "untracked.txt"), []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := CheckoutBranch(ctx, root, "new-branch", true); err != nil {
		t.Fatalf("untracked file blocked checkout: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("dirty\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := CheckoutBranch(ctx, root, current, false); err == nil {
		t.Fatal("tracked dirty state did not block branch checkout")
	}
	if err := Stash(ctx, root, true); err != nil {
		t.Fatal(err)
	}
	if status := strings.TrimSpace(runGitOutput(t, root, "status", "--porcelain")); status != "" {
		t.Fatalf("stash left dirty state: %s", status)
	}
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("second\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "tracked.txt")
	runGit(t, root, "commit", "-qm", "second")
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("pending\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	outcome := CheckoutCommit(ctx, root, initial, true)
	if !outcome.CheckedOut || !outcome.Stashed || outcome.Fetched || outcome.Error != "" {
		t.Fatalf("unexpected checkout commit outcome: %#v", outcome)
	}
	if got := strings.TrimSpace(runGitOutput(t, root, "rev-parse", "HEAD")); got != initial {
		t.Fatalf("checkout commit HEAD=%q want=%q", got, initial)
	}
}
