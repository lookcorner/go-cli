package worktree

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestManagerLinkedDirtyLifecycleAndPersistence(t *testing.T) {
	root := newRepo(t)
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("modified\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "staged.txt"), []byte("staged\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "staged.txt")
	if err := os.WriteFile(filepath.Join(root, "untracked.txt"), []byte("untracked\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	state := t.TempDir()
	dest := filepath.Join(t.TempDir(), "linked")
	manager, err := NewManager(state)
	if err != nil {
		t.Fatal(err)
	}
	record, existed, err := manager.Create(context.Background(), CreateRequest{
		SessionID: "session-1", SourcePath: root, WorktreePath: dest,
		CopyMode: "dirty", WorktreeType: "linked", Label: "Feature One",
	})
	if err != nil || existed {
		t.Fatalf("create linked: record=%#v existed=%v err=%v", record, existed, err)
	}
	for name, want := range map[string]string{
		"tracked.txt": "modified\n", "staged.txt": "staged\n", "untracked.txt": "untracked\n",
	} {
		data, err := os.ReadFile(filepath.Join(dest, name))
		if err != nil || string(data) != want {
			t.Fatalf("%s = %q, want %q (err=%v)", name, data, want, err)
		}
	}
	status := runGitOutput(t, dest, "status", "--porcelain")
	if !strings.Contains(status, "A  staged.txt") || !strings.Contains(status, " M tracked.txt") || !strings.Contains(status, "?? untracked.txt") {
		t.Fatalf("dirty state was not preserved:\n%s", status)
	}
	reloaded, err := NewManager(state)
	if err != nil {
		t.Fatal(err)
	}
	shown, ok := reloaded.Show(record.ID)
	if !ok || shown.Path != dest || len(reloaded.List("", nil, false)) != 1 {
		t.Fatalf("persisted record missing: shown=%#v ok=%v", shown, ok)
	}
	removed, resolved, err := reloaded.Remove(context.Background(), RemoveRequest{IDOrPath: record.ID, Force: true})
	if err != nil || !removed || resolved != dest {
		t.Fatalf("remove linked: removed=%v path=%q err=%v", removed, resolved, err)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatalf("linked worktree still exists: %v", err)
	}
}

func TestCopyEntryPreservesModeAndCreatesIndependentFile(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source")
	dest := filepath.Join(dir, "nested", "dest")
	if err := os.WriteFile(source, []byte("original\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := copyEntry(source, dest); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("copied mode = %o, want 755", info.Mode().Perm())
	}
	if err := os.WriteFile(dest, []byte("changed\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(source)
	if err != nil || string(data) != "original\n" {
		t.Fatalf("source changed with clone: %q err=%v", data, err)
	}
}

func TestCopyEntryDoesNotReplaceExistingFile(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source")
	dest := filepath.Join(dir, "dest")
	if err := os.WriteFile(source, []byte("source\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, []byte("existing\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := copyEntry(source, dest); err == nil {
		t.Fatal("copy replaced an existing destination")
	}
	data, err := os.ReadFile(dest)
	if err != nil || string(data) != "existing\n" {
		t.Fatalf("existing destination changed: %q err=%v", data, err)
	}
}

func TestManagerStandaloneAndSafeRemove(t *testing.T) {
	root := newRepo(t)
	manager, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "standalone")
	record, _, err := manager.Create(context.Background(), CreateRequest{
		SessionID: "session-2", SourcePath: root, WorktreePath: dest,
		CopyMode: "clean", WorktreeType: "standalone",
	})
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dest, ".git"))
	if err != nil || !info.IsDir() {
		t.Fatalf("standalone .git is not a directory: %v", err)
	}
	removed, resolved, err := manager.Remove(context.Background(), RemoveRequest{IDOrPath: record.ID, DryRun: true})
	if err != nil || removed || resolved != dest {
		t.Fatalf("dry run: removed=%v path=%q err=%v", removed, resolved, err)
	}
	outside := t.TempDir()
	if _, _, err := manager.Remove(context.Background(), RemoveRequest{WorktreePath: outside, Force: true}); err == nil {
		t.Fatal("unregistered directory was accepted for removal")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("unregistered directory was touched: %v", err)
	}
	if _, _, err := manager.Remove(context.Background(), RemoveRequest{IDOrPath: record.ID}); err != nil {
		t.Fatal(err)
	}
}

func TestManagerApplyOverwrite(t *testing.T) {
	root := newRepo(t)
	manager, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "apply-overwrite")
	record, _, err := manager.Create(context.Background(), CreateRequest{
		SessionID: "apply-1", SourcePath: root, WorktreePath: dest, CopyMode: "clean", WorktreeType: "linked",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _, _ = manager.Remove(context.Background(), RemoveRequest{IDOrPath: record.ID, Force: true})
	})
	if err := os.WriteFile(filepath.Join(dest, "tracked.txt"), []byte("from worktree\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "new.txt"), []byte("new\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	response, err := manager.Apply(context.Background(), ApplyRequest{
		SessionID: "apply-1", WorktreePath: dest, Mode: "overwrite",
	})
	if err != nil || response.Status != "success" || len(response.Files) != 2 || response.GitRoot != record.SourceRepo {
		t.Fatalf("apply overwrite: %#v err=%v", response, err)
	}
	for name, want := range map[string]string{"tracked.txt": "from worktree\n", "new.txt": "new\n"} {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil || string(data) != want {
			t.Fatalf("applied %s = %q, want %q (err=%v)", name, data, want, err)
		}
	}
}

func TestManagerApplyMergeReportsConflictsAndAppliesSafeFiles(t *testing.T) {
	root := newRepo(t)
	manager, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "apply-merge")
	record, _, err := manager.Create(context.Background(), CreateRequest{
		SessionID: "apply-2", SourcePath: root, WorktreePath: dest, CopyMode: "clean", WorktreeType: "linked",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _, _ = manager.Remove(context.Background(), RemoveRequest{IDOrPath: record.ID, Force: true})
	})
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("ours\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "tracked.txt"), []byte("theirs\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "safe.txt"), []byte("safe\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	response, err := manager.Apply(context.Background(), ApplyRequest{
		SessionID: "apply-2", WorktreePath: dest, Mode: "merge",
	})
	if err != nil || response.Status != "conflicts" || len(response.Conflicts) != 1 || len(response.Files) != 1 {
		t.Fatalf("apply merge: %#v err=%v", response, err)
	}
	conflict := response.Conflicts[0]
	if conflict.Path != "tracked.txt" || conflict.Base == nil || *conflict.Base != "original\n" || conflict.Ours == nil || *conflict.Ours != "ours\n" || conflict.Theirs == nil || *conflict.Theirs != "theirs\n" {
		t.Fatalf("unexpected conflict: %#v", conflict)
	}
	data, err := os.ReadFile(filepath.Join(root, "tracked.txt"))
	if err != nil || string(data) != "ours\n" {
		t.Fatalf("conflict overwrote ours: %q err=%v", data, err)
	}
	data, err = os.ReadFile(filepath.Join(root, "safe.txt"))
	if err != nil || string(data) != "safe\n" {
		t.Fatalf("safe file was not applied: %q err=%v", data, err)
	}
}

func TestManagerCreateFromLinkedWorktreeKeepsMainRepoIdentity(t *testing.T) {
	root := newRepo(t)
	manager, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	firstPath := filepath.Join(t.TempDir(), "first")
	first, _, err := manager.Create(context.Background(), CreateRequest{
		SessionID: "parent", SourcePath: root, WorktreePath: firstPath, CopyMode: "clean", WorktreeType: "linked",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(firstPath, "tracked.txt"), []byte("fork dirty\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fork, _, err := manager.CreateFromWorktree(context.Background(), ForkRequest{
		SourceWorktreePath: firstPath, NewSessionID: "child", CopyMode: "dirty", WorktreeType: "linked", Label: "child",
	})
	if err != nil {
		t.Fatal(err)
	}
	child, ok := manager.Show(fork.WorktreePath)
	if !ok || child.SourceRepo != first.SourceRepo || child.SourceRepo == firstPath {
		t.Fatalf("fork source identity is nested: parent=%#v child=%#v", first, child)
	}
	data, err := os.ReadFile(filepath.Join(fork.WorktreePath, "tracked.txt"))
	if err != nil || string(data) != "fork dirty\n" {
		t.Fatalf("fork dirty state missing: %q err=%v", data, err)
	}
	if _, _, err := manager.Remove(context.Background(), RemoveRequest{WorktreePath: fork.WorktreePath, Force: true}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := manager.Remove(context.Background(), RemoveRequest{IDOrPath: first.ID, Force: true}); err != nil {
		t.Fatal(err)
	}
}

func TestManagerGCStatsAndRebuild(t *testing.T) {
	root := newRepo(t)
	manager, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(manager.base, "repo", "managed")
	record, _, err := manager.Create(context.Background(), CreateRequest{
		SessionID: "gc-session", SourcePath: root, WorktreePath: dest, CopyMode: "clean", WorktreeType: "linked",
	})
	if err != nil {
		t.Fatal(err)
	}
	manager.mu.Lock()
	record.LastAccessedAt = time.Now().Add(-48 * time.Hour)
	record.CreatorPID = 0
	manager.records[record.ID] = record
	if err := manager.save(); err != nil {
		manager.mu.Unlock()
		t.Fatal(err)
	}
	manager.mu.Unlock()
	stats := manager.Stats()
	if stats.TotalRecords != 1 || stats.AliveCount != 1 || stats.DBFileBytes == 0 {
		t.Fatalf("unexpected stats: %#v", stats)
	}
	age := 24 * time.Hour
	dry, err := manager.GC(context.Background(), true, &age, true)
	if err != nil || dry.ExpiredRemoved != 1 {
		t.Fatalf("dry GC: %#v err=%v", dry, err)
	}
	if _, err := os.Stat(dest); err != nil || len(manager.List("", nil, true)) != 1 {
		t.Fatalf("dry GC mutated state: stat=%v records=%d", err, len(manager.List("", nil, true)))
	}

	manager.mu.Lock()
	delete(manager.records, record.ID)
	if err := manager.save(); err != nil {
		manager.mu.Unlock()
		t.Fatal(err)
	}
	manager.mu.Unlock()
	rebuilt, err := manager.Rebuild(context.Background())
	if err != nil || rebuilt.Discovered != 1 || rebuilt.Registered != 1 {
		t.Fatalf("rebuild: %#v err=%v", rebuilt, err)
	}
	items := manager.List("", nil, false)
	if len(items) != 1 {
		t.Fatalf("rebuild records: %#v", items)
	}
	manager.mu.Lock()
	rebuiltRecord := items[0]
	rebuiltRecord.LastAccessedAt = time.Now().Add(-48 * time.Hour)
	rebuiltRecord.CreatorPID = 0
	manager.records[rebuiltRecord.ID] = rebuiltRecord
	manager.mu.Unlock()
	real, err := manager.GC(context.Background(), false, &age, true)
	if err != nil || real.ExpiredRemoved != 1 {
		t.Fatalf("real GC: %#v err=%v", real, err)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatalf("expired worktree survived GC: %v", err)
	}
}

func TestSanitizeLabel(t *testing.T) {
	if got := sanitizeLabel("  Feature__One..!  "); got != "feature-one" {
		t.Fatalf("sanitizeLabel = %q", got)
	}
}

func newRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	runGit(t, root, "init", "-q")
	runGit(t, root, "config", "user.name", "Fixture")
	runGit(t, root, "config", "user.email", "fixture@example.invalid")
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("original\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "tracked.txt")
	runGit(t, root, "commit", "-qm", "baseline")
	return root
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	_ = runGitOutput(t, dir, args...)
}

func runGitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return string(output)
}
