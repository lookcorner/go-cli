package worktree

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSnapshotRehydrateRoundTrip(t *testing.T) {
	ctx := context.Background()
	root := newRepo(t)
	for name, content := range map[string]string{"delete.txt": "delete\n", "ignored.txt": "tracked\n"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	runGit(t, root, "add", "delete.txt", "ignored.txt")
	runGit(t, root, "commit", "-qm", "tracked snapshot fixtures")
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("ignored.txt\ncache/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", ".gitignore")
	runGit(t, root, "commit", "-qm", "snapshot ignore rules")
	manager, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "worktree")
	record, _, err := manager.Create(ctx, CreateRequest{SessionID: "source", SourcePath: root, WorktreePath: dest, CopyMode: "clean", WorktreeType: "linked"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "tracked.txt"), []byte("modified\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "staged.txt"), []byte("staged\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, dest, "add", "staged.txt")
	if err := os.Remove(filepath.Join(dest, "delete.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "ignored.txt"), []byte("tracked but ignored\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "lambda space.txt"), []byte("unicode path\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dest, "cache"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "cache", "ignored.bin"), []byte("ignored\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	statusBefore := runGitOutput(t, dest, "status", "--porcelain")
	ref := "refs/gork/snapshots/roundtrip"
	first, err := SnapshotToRef(ctx, dest, ref, "first snapshot")
	if err != nil {
		t.Fatal(err)
	}
	if statusAfter := runGitOutput(t, dest, "status", "--porcelain"); statusAfter != statusBefore {
		t.Fatalf("snapshot changed real index:\nbefore=%s\nafter=%s", statusBefore, statusAfter)
	}
	if err := os.WriteFile(filepath.Join(dest, "tracked.txt"), []byte("latest\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := SnapshotToRef(ctx, dest, ref, "second snapshot")
	if err != nil || second == first {
		t.Fatalf("snapshot overwrite: first=%q second=%q err=%v", first, second, err)
	}
	if err := TransferSnapshot(ctx, dest, root, ref); err != nil {
		t.Fatalf("transfer linked snapshot: %v", err)
	}
	runGit(t, root, "worktree", "remove", "--force", record.Path)
	rehydrated, err := manager.Rehydrate(ctx, RehydrateRequest{SessionID: "resumed", SourceRepo: root, WorktreePath: dest, SnapshotRef: ref})
	if err != nil {
		t.Fatal(err)
	}
	if rehydrated.Kind != "subagent" || rehydrated.SessionID != "resumed" || rehydrated.Path != dest {
		t.Fatalf("unexpected record: %#v", rehydrated)
	}
	for name, want := range map[string]string{
		"tracked.txt": "latest\r\n", "staged.txt": "staged\n", "ignored.txt": "tracked but ignored\n", "lambda space.txt": "unicode path\n",
	} {
		data, err := os.ReadFile(filepath.Join(dest, name))
		if err != nil || string(data) != want {
			t.Fatalf("restored %s=%q want=%q err=%v", name, data, want, err)
		}
	}
	for _, name := range []string{"delete.txt", filepath.Join("cache", "ignored.bin")} {
		if _, err := os.Stat(filepath.Join(dest, name)); !os.IsNotExist(err) {
			t.Fatalf("unexpected restored path %s: %v", name, err)
		}
	}
	base := strings.TrimSpace(runGitOutput(t, root, "rev-parse", second+"^"))
	if head := strings.TrimSpace(runGitOutput(t, dest, "rev-parse", "HEAD")); head != base || rehydrated.HeadCommit != base {
		t.Fatalf("rehydrated HEAD=%q record=%q want base=%q", head, rehydrated.HeadCommit, base)
	}
	if _, ok := manager.Show(rehydrated.ID); !ok {
		t.Fatal("rehydrated worktree was not registered")
	}
	if records := manager.List("", nil, true); len(records) != 1 {
		t.Fatalf("rehydrate left duplicate records: %#v", records)
	}
}

func TestStandaloneSnapshotTransferSurvivesRemoval(t *testing.T) {
	ctx := context.Background()
	root := newRepo(t)
	manager, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "standalone")
	record, _, err := manager.Create(ctx, CreateRequest{SessionID: "standalone", SourcePath: root, WorktreePath: dest, CopyMode: "clean", WorktreeType: "standalone"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "tracked.txt"), []byte("standalone edit\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "new.txt"), []byte("new\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ref := "refs/gork/subagents/standalone"
	commit, err := SnapshotToRef(ctx, dest, ref, "standalone snapshot")
	if err != nil {
		t.Fatal(err)
	}
	if err := TransferSnapshot(ctx, dest, root, ref); err != nil {
		t.Fatal(err)
	}
	if _, _, err := manager.Remove(ctx, RemoveRequest{IDOrPath: record.ID, Force: true}); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(runGitOutput(t, root, "rev-parse", ref)); got != commit {
		t.Fatalf("transferred ref=%q want=%q", got, commit)
	}
	if _, err := manager.Rehydrate(ctx, RehydrateRequest{SessionID: "resumed", SourceRepo: root, WorktreePath: dest, SnapshotRef: ref}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dest, "new.txt"))
	if err != nil || string(data) != "new\n" {
		t.Fatalf("standalone snapshot not restored: %q err=%v", data, err)
	}
	if err := DeleteSnapshotRef(ctx, root, ref); err != nil {
		t.Fatal(err)
	}
	if commandOutput := runGitMaybe(root, "rev-parse", "--verify", ref); commandOutput == nil {
		t.Fatal("snapshot ref still exists after deletion")
	}
}

func TestRehydrateFailureDoesNotCreateDestination(t *testing.T) {
	root := newRepo(t)
	manager, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "missing")
	_, err = manager.Rehydrate(context.Background(), RehydrateRequest{SessionID: "bad", SourceRepo: root, WorktreePath: dest, SnapshotRef: "refs/gork/missing"})
	if err == nil {
		t.Fatal("missing snapshot was accepted")
	}
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Fatalf("failed rehydrate left destination: %v", statErr)
	}
}

func runGitMaybe(dir string, args ...string) error {
	command := exec.Command("git", args...)
	command.Dir = dir
	return command.Run()
}
