package worktree

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDetectHeadDivergence(t *testing.T) {
	for name, input := range map[string][3]string{
		"missing session commit": {"", "main", "current"},
		"missing current commit": {"session", "main", ""},
		"same commit":            {"same", "main", "same"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := DetectHeadDivergence(input[0], input[1], input[2]); got != nil {
				t.Fatalf("divergence=%#v", got)
			}
		})
	}

	got := DetectHeadDivergence("session", "main", "current")
	if got == nil || got.SessionCommit != "session" || got.CurrentCommit != "current" || got.SessionBranch != "main" {
		t.Fatalf("divergence=%#v", got)
	}
	encoded, err := json.Marshal(got)
	if err != nil || string(encoded) != `{"sessionCommit":"session","currentCommit":"current","sessionBranch":"main"}` {
		t.Fatalf("json=%s err=%v", encoded, err)
	}

	withoutBranch, err := json.Marshal(DetectHeadDivergence("session", "", "current"))
	if err != nil || string(withoutBranch) != `{"sessionCommit":"session","currentCommit":"current"}` {
		t.Fatalf("json without branch=%s err=%v", withoutBranch, err)
	}
}

func TestHeadInfoAndWatcher(t *testing.T) {
	root := newRepo(t)
	branch := strings.TrimSpace(runGitOutput(t, root, "branch", "--show-current"))
	head, err := HeadInfo(context.Background(), root)
	if err != nil || head.Branch != branch || head.IsWorktree || head.Root != head.MainRoot {
		t.Fatalf("head=%#v err=%v", head, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan struct{})
	changed := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		done <- WatchHead(ctx, root, func() { close(ready) }, func() {
			select {
			case changed <- struct{}{}:
			default:
			}
		})
	}()
	select {
	case <-ready:
	case <-time.After(3 * time.Second):
		t.Fatal("git head watcher did not start")
	}
	runGit(t, root, "checkout", "-qb", "feature/head-watch")
	select {
	case <-changed:
	case <-time.After(3 * time.Second):
		t.Fatal("git head watcher missed branch checkout")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestWatchHeadDiscoversInitializedRepository(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan struct{})
	changed := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		done <- WatchHead(ctx, root, func() { close(ready) }, func() {
			select {
			case changed <- struct{}{}:
			default:
			}
		})
	}()
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("git head watcher failed to start: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("git head watcher did not start")
	}
	runGit(t, root, "init", "-q")
	select {
	case <-changed:
	case <-time.After(3 * time.Second):
		t.Fatal("git head watcher missed repository initialization")
	}
	if _, err := HeadInfo(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestWatchHeadRejectsMissingWorkspace(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	if err := WatchHead(context.Background(), missing, nil, nil); err == nil || !strings.Contains(err.Error(), "watch Git HEAD") {
		t.Fatalf("missing workspace error=%v", err)
	}
}

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
	status, err := Status(ctx, root, true, false, true, true)
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
	if status.Staged[0].Patch == nil || !strings.Contains(*status.Staged[0].Patch, "+staged") || status.Staged[0].PatchBytes == nil || *status.Staged[0].PatchBytes == 0 || status.Staged[0].PatchLines == nil || *status.Staged[0].PatchLines == 0 {
		t.Fatalf("missing staged patch: %#v", status.Staged[0])
	}
	if len(status.Unstaged) != 2 || status.Unstaged[0].Path != "both.txt" || status.Unstaged[0].Staged || status.Unstaged[1].Path != "new.txt" || status.Unstaged[1].Type != "untracked" {
		t.Fatalf("unexpected unstaged status: %#v", status.Unstaged)
	}
	if status.Unstaged[0].Additions != 1 || status.Unstaged[0].Deletions != 1 || status.Unstaged[0].Patch == nil || !strings.Contains(*status.Unstaged[0].Patch, "+working") {
		t.Fatalf("missing unstaged patch and implicit stats: %#v", status.Unstaged[0])
	}
	if status.Unstaged[1].Patch != nil || status.Unstaged[1].PatchBytes != nil || status.Unstaged[1].PatchLines != nil {
		t.Fatalf("untracked file unexpectedly has patch data: %#v", status.Unstaged[1])
	}
	if _, err := Stage(ctx, root, []string{"new.txt"}); err != nil {
		t.Fatal(err)
	}
	if err := Unstage(ctx, root, []string{"new.txt"}); err != nil {
		t.Fatal(err)
	}
	status, err = Status(ctx, root, false, false, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Unstaged) != 1 || status.Unstaged[0].Path != "both.txt" {
		t.Fatalf("includeUntracked=false status: %#v", status.Unstaged)
	}
	if status.Unstaged[0].Additions != 0 || status.Unstaged[0].Deletions != 0 || status.Unstaged[0].Patch != nil || status.Unstaged[0].PatchBytes != nil || status.Unstaged[0].PatchLines != nil {
		t.Fatalf("patches and stats included when disabled: %#v", status.Unstaged[0])
	}
	if err := Discard(ctx, root, nil, "both", true); err != nil {
		t.Fatal(err)
	}
	status, err = Status(ctx, root, true, false, true, false)
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

func TestGitStatusFiltersNestedRepositories(t *testing.T) {
	ctx := context.Background()
	root := newRepo(t)
	nested := filepath.Join(root, "nested")
	if err := os.Mkdir(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	runGit(t, nested, "init", "-q")
	if err := os.WriteFile(filepath.Join(nested, "file.txt"), []byte("nested\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ignored, err := Status(ctx, root, true, false, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(ignored.Unstaged) != 0 {
		t.Fatalf("nested repository was not ignored: %#v", ignored.Unstaged)
	}

	included, err := Status(ctx, root, true, false, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(included.Unstaged) != 1 || included.Unstaged[0].Path != "nested/" || included.Unstaged[0].Type != "untracked" {
		t.Fatalf("nested repository was not included: %#v", included.Unstaged)
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

func TestCommitReturnsHashAndSignoff(t *testing.T) {
	root := newRepo(t)
	if err := os.WriteFile(filepath.Join(root, "commit.txt"), []byte("commit me\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Stage(context.Background(), root, []string{"commit.txt"}); err != nil {
		t.Fatal(err)
	}
	data, warning, err := Commit(context.Background(), root, "add commit fixture", false, true, false, false)
	if err != nil || warning != "" || data.CommitHash == "" || !strings.HasPrefix(data.Output, "Committed: ") {
		t.Fatalf("commit data=%#v warning=%q err=%v", data, warning, err)
	}
	if got := strings.TrimSpace(runGitOutput(t, root, "rev-parse", "HEAD")); got != data.CommitHash {
		t.Fatalf("commit hash=%q want=%q", data.CommitHash, got)
	}
	message := runGitOutput(t, root, "log", "-1", "--format=%B")
	if !strings.Contains(message, "Signed-off-by: Fixture <fixture@example.invalid>") {
		t.Fatalf("signoff missing from commit message: %s", message)
	}
}

func TestReadFilesAndStageContent(t *testing.T) {
	ctx := context.Background()
	root := newRepo(t)
	if err := os.Chmod(filepath.Join(root, "tracked.txt"), 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "tracked.txt")
	runGit(t, root, "commit", "-qm", "make executable")
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("working\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "binary.bin"), []byte{0xff, 0x00, 0x01}, 0o600); err != nil {
		t.Fatal(err)
	}
	head, err := ReadFiles(ctx, root, []string{"tracked.txt"}, "HEAD")
	if err != nil || len(head.Files) != 1 || head.Files[0].Content != "original\n" || head.Files[0].IsBinary {
		t.Fatalf("HEAD files=%#v err=%v", head, err)
	}
	working, err := ReadFiles(ctx, root, []string{"tracked.txt", "binary.bin", "missing.txt"}, "working")
	if err != nil || len(working.Files) != 2 || len(working.Errors) != 1 || !working.Files[0].IsBinary && !working.Files[1].IsBinary {
		t.Fatalf("working files=%#v err=%v", working, err)
	}
	if err := StageContent(ctx, root, "tracked.txt", "staged only\n"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "tracked.txt"))
	if err != nil || string(data) != "working\n" {
		t.Fatalf("stage content changed working file: %q err=%v", data, err)
	}
	staged, err := ReadFiles(ctx, root, []string{"tracked.txt"}, "staged")
	if err != nil || len(staged.Files) != 1 || staged.Files[0].Content != "staged only\n" {
		t.Fatalf("staged files=%#v err=%v", staged, err)
	}
	if mode := strings.Fields(runGitOutput(t, root, "ls-files", "-s", "--", "tracked.txt"))[0]; mode != "100755" {
		t.Fatalf("stage content mode=%q want=100755", mode)
	}
	if _, err := ReadFiles(ctx, root, []string{filepath.Join(t.TempDir(), "outside.txt")}, "working"); err == nil {
		t.Fatal("repository-external path was accepted")
	}
}

func TestDiffsAcrossCommitsIndexAndWorkingTree(t *testing.T) {
	ctx := context.Background()
	root := newRepo(t)
	base := strings.TrimSpace(runGitOutput(t, root, "rev-parse", "HEAD"))
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("committed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "tracked.txt")
	runGit(t, root, "commit", "-qm", "second")
	head := strings.TrimSpace(runGitOutput(t, root, "rev-parse", "HEAD"))
	commits, err := Diffs(ctx, root, nil, base, head, true, true, false)
	if err != nil || len(commits.Files) != 1 {
		t.Fatalf("commit diff=%#v err=%v", commits, err)
	}
	file := commits.Files[0]
	if file.Path != "tracked.txt" || file.Type != "edit" || file.Additions != 1 || file.Deletions != 1 || file.Patch == nil || !strings.Contains(*file.Patch, "+committed") || file.OldText == nil || *file.OldText != "original\n" || file.NewText == nil || *file.NewText != "committed\n" {
		t.Fatalf("unexpected commit diff file: %#v", file)
	}
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("working\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := StageContent(ctx, root, "tracked.txt", "staged\n"); err != nil {
		t.Fatal(err)
	}
	staged, err := Diffs(ctx, root, []string{"tracked.txt"}, "HEAD", "staged", false, true, false)
	if err != nil || len(staged.Files) != 1 || staged.Files[0].NewText == nil || *staged.Files[0].NewText != "staged\n" {
		t.Fatalf("staged diff=%#v err=%v", staged, err)
	}
	working, err := Diffs(ctx, root, nil, "staged", "working", false, true, false)
	if err != nil || len(working.Files) != 1 || working.Files[0].OldText == nil || *working.Files[0].OldText != "staged\n" || working.Files[0].NewText == nil || *working.Files[0].NewText != "working\n" {
		t.Fatalf("working diff=%#v err=%v", working, err)
	}
}
