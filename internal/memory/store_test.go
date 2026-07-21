package memory

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStoreWritesDeduplicatesAndBuildsBoundedContext(t *testing.T) {
	root, workspace := t.TempDir(), t.TempDir()
	store, err := Open(root, workspace, "session-one")
	if err != nil {
		t.Fatal(err)
	}
	content := "## Decisions\n\nUse a small domain store."
	path, written, err := store.Write("pre_compaction", content)
	if err != nil || !written {
		t.Fatalf("path=%q written=%v err=%v", path, written, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("memory mode=%v", info.Mode().Perm())
	}
	if duplicatePath, duplicate, err := store.Write("pre_compaction", content); err != nil || duplicate || duplicatePath != path {
		t.Fatalf("duplicate path=%q written=%v err=%v", duplicatePath, duplicate, err)
	}
	if err := os.WriteFile(filepath.Join(store.workspaceDir, "MEMORY.md"), []byte("## Conventions\n\nKeep boundaries clear.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.root, "MEMORY.md"), []byte("## Global\n\nUse concise answers.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, written, err := store.Write("pre_compaction", "## Long\n\n"+strings.Repeat("x", maxSnippetChars+100)); err != nil || !written {
		t.Fatalf("long memory written=%v err=%v", written, err)
	}
	context, err := store.Context()
	if err != nil || !strings.Contains(context, "<memory-context>") || !strings.Contains(context, "Keep boundaries clear") || !strings.Contains(context, "Use a small domain store") {
		t.Fatalf("context=%q err=%v", context, err)
	}
	wantRun := maxSnippetChars - len([]rune("## Long\n\n"))
	if strings.Contains(context, strings.Repeat("x", maxSnippetChars+1)) || !strings.Contains(context, strings.Repeat("x", wantRun)+"...") {
		t.Fatalf("context did not apply the per-result bound: %q", context)
	}
	items, err := os.ReadDir(store.sessionsDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if strings.HasPrefix(item.Name(), ".tmp-") {
			t.Fatalf("atomic write left temporary file %q", item.Name())
		}
	}
	files, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 4 || files[0].Source != "global" || files[1].Source != "workspace" || files[2].Source != "session" || files[0].Path != filepath.Join(store.root, "MEMORY.md") || files[0].SizeBytes == 0 || files[0].ModifiedEpochSeconds == nil {
		t.Fatalf("files=%#v", files)
	}
}

func TestStoreSeparatesWorkspacesAndRejectsSymlinkSources(t *testing.T) {
	root := t.TempDir()
	first, err := Open(root, t.TempDir(), "first")
	if err != nil {
		t.Fatal(err)
	}
	second, err := Open(root, t.TempDir(), "second")
	if err != nil {
		t.Fatal(err)
	}
	if first.workspaceDir == second.workspaceDir {
		t.Fatal("distinct workspaces shared memory")
	}
	target := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(target, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(first.sessionsDir, "escape.md")); err != nil {
		t.Fatal(err)
	}
	context, err := first.Context()
	if err != nil || strings.Contains(context, "secret") {
		t.Fatalf("symlink content escaped: context=%q err=%v", context, err)
	}
	files, err := first.List()
	if err != nil || len(files) != 0 {
		t.Fatalf("symlink appeared in list: files=%#v err=%v", files, err)
	}
}

func TestAppendGlobalNormalizesAndRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	path, err := AppendGlobal(root, "prefer tabs")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AppendGlobal(root, "Release Process\nRun checks before deploy."); err != nil {
		t.Fatal(err)
	}
	if _, err := AppendGlobal(root, "## Existing\n\nKeep this structure."); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "## prefer tabs\n\n## Release Process\n\nRun checks before deploy.\n\n## Existing\n\nKeep this structure."
	if string(data) != want {
		t.Fatalf("global memory=%q", data)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%v", info.Mode().Perm())
	}
	if _, err := AppendGlobal(root, "   "); err == nil {
		t.Fatal("empty note was accepted")
	}
	fullRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(fullRoot, "MEMORY.md"), []byte(strings.Repeat("x", maxMemoryFileBytes)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := AppendGlobal(fullRoot, "one more note"); err == nil {
		t.Fatal("oversized global memory was accepted")
	}

	outside := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}
	if _, err := AppendGlobal(root, "escape"); err == nil {
		t.Fatal("symlink global memory was accepted")
	}
	if data, err := os.ReadFile(outside); err != nil || string(data) != "secret" {
		t.Fatalf("outside=%q err=%v", data, err)
	}
}

func TestClearMemoryScopesAndRejectsSymlinks(t *testing.T) {
	root, workspace := t.TempDir(), t.TempDir()
	store, err := Open(root, workspace, "clear")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Write("manual", "workspace note"); err != nil {
		t.Fatal(err)
	}
	global, err := AppendGlobal(root, "global note")
	if err != nil {
		t.Fatal(err)
	}
	if cleared, err := ClearWorkspace(root, workspace); err != nil || !cleared {
		t.Fatalf("cleared=%v err=%v", cleared, err)
	}
	if _, err := os.Stat(store.workspaceDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("workspace still exists: %v", err)
	}
	if _, err := os.Stat(global); err != nil {
		t.Fatalf("global memory was removed: %v", err)
	}
	if cleared, err := ClearWorkspace(root, workspace); err != nil || cleared {
		t.Fatalf("second clear=%v err=%v", cleared, err)
	}
	if cleared, err := ClearGlobal(root); err != nil || !cleared {
		t.Fatalf("global clear=%v err=%v", cleared, err)
	}

	outside := t.TempDir()
	workspacePath, err := WorkspacePath(root, workspace)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, workspacePath); err != nil {
		t.Fatal(err)
	}
	if _, err := ClearWorkspace(root, workspace); err == nil {
		t.Fatal("workspace symlink was cleared")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("symlink target changed: %v", err)
	}

	outsideRoot := t.TempDir()
	outsideWorkspace, err := WorkspacePath(outsideRoot, workspace)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outsideWorkspace, 0o700); err != nil {
		t.Fatal(err)
	}
	linkedRoot := filepath.Join(t.TempDir(), "memory")
	if err := os.Symlink(outsideRoot, linkedRoot); err != nil {
		t.Fatal(err)
	}
	if _, err := ClearWorkspace(linkedRoot, workspace); err == nil {
		t.Fatal("symlink memory root was followed")
	}
	if _, err := os.Stat(outsideWorkspace); err != nil {
		t.Fatalf("linked root target changed: %v", err)
	}
}

func TestStoreGCRemovesOnlyEligibleWorkspaceDirectories(t *testing.T) {
	root, workspace := t.TempDir(), t.TempDir()
	store, err := Open(root, workspace, "gc")
	if err != nil {
		t.Fatal(err)
	}
	create := func(name string, session, old bool) string {
		t.Helper()
		path := filepath.Join(root, name)
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if session {
			if err := os.MkdirAll(filepath.Join(path, "sessions"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(path, "sessions", "log.md"), []byte("memory"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if old {
			when := time.Now().Add(-31 * 24 * time.Hour)
			if strings.HasPrefix(name, "tmp") {
				when = time.Now().Add(-8 * 24 * time.Hour)
			}
			if err := os.Chtimes(path, when, when); err != nil {
				t.Fatal(err)
			}
		}
		return path
	}
	removeEmptyTmp := create("tmp-empty", false, false)
	keepYoungTmp := create("tmp-young", true, false)
	removeOldTmp := create("tmp-old", true, true)
	removeOldEmpty := create("old-empty", false, true)
	if err := os.WriteFile(filepath.Join(removeOldEmpty, "MEMORY.md"), []byte("project"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(removeOldEmpty, "index.sqlite"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	oldEmptyTime := time.Now().Add(-31 * 24 * time.Hour)
	if err := os.Chtimes(removeOldEmpty, oldEmptyTime, oldEmptyTime); err != nil {
		t.Fatal(err)
	}
	keepYoungEmpty := create("young-empty", false, false)
	keepActive := create("old-active", true, true)
	rootFile := filepath.Join(root, "MEMORY.md")
	if err := os.WriteFile(rootFile, []byte("global"), 0o600); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(root, "tmp-linked")
	if err := os.Symlink(t.TempDir(), linked); err != nil {
		t.Fatal(err)
	}
	currentOld := time.Now().Add(-60 * 24 * time.Hour)
	if err := os.Chtimes(store.workspaceDir, currentOld, currentOld); err != nil {
		t.Fatal(err)
	}

	removed, err := store.GC(30)
	if err != nil || removed != 3 {
		t.Fatalf("removed=%d err=%v", removed, err)
	}
	for _, path := range []string{removeEmptyTmp, removeOldTmp, removeOldEmpty} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("eligible path remains %s: %v", path, err)
		}
	}
	for _, path := range []string{keepYoungTmp, keepYoungEmpty, keepActive, store.workspaceDir, rootFile, linked} {
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("protected path removed %s: %v", path, err)
		}
	}
}

func TestStoreGCHandlesMissingAndUnsafeRoots(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	store := &Store{root: missing, workspaceDir: filepath.Join(missing, "current")}
	if removed, err := store.GC(30); err != nil || removed != 0 {
		t.Fatalf("missing root removed=%d err=%v", removed, err)
	}
	outside := t.TempDir()
	linked := filepath.Join(t.TempDir(), "memory")
	if err := os.Symlink(outside, linked); err != nil {
		t.Fatal(err)
	}
	store.root = linked
	if _, err := store.GC(30); err == nil {
		t.Fatal("symlink memory root was accepted")
	}
}

func TestOpenWorkspaceSkipsEphemeralPersistenceButKeepsGlobalMemory(t *testing.T) {
	root, workspace := t.TempDir(), t.TempDir()
	store, err := OpenWorkspace(root, workspace, "ephemeral")
	if err != nil || !store.IsEphemeral() {
		t.Fatalf("store=%#v err=%v", store, err)
	}
	if _, err := os.Stat(store.workspaceDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ephemeral workspace directory exists: %v", err)
	}
	path, written, err := store.Write("session_end", "temporary knowledge")
	if err != nil || written || filepath.Dir(path) != store.sessionsDir {
		t.Fatalf("path=%q written=%v err=%v", path, written, err)
	}
	global, err := AppendGlobal(root, "global convention\n\nshared across workspaces")
	if err != nil {
		t.Fatal(err)
	}
	files, err := store.List()
	if err != nil || len(files) != 1 || files[0].Source != "global" {
		t.Fatalf("files=%#v err=%v", files, err)
	}
	context, err := store.Context()
	if err != nil || !strings.Contains(context, "global convention") {
		t.Fatalf("context=%q err=%v", context, err)
	}
	results, err := store.Search("global convention", DefaultConfig().Index, DefaultConfig().Search)
	if err != nil || len(results) != 1 || results[0].Source != "global" {
		t.Fatalf("results=%#v err=%v", results, err)
	}
	if file, err := store.Get(global, 0, 1); err != nil || len(file.Lines) != 1 {
		t.Fatalf("file=%#v err=%v", file, err)
	}
	if _, result, err := store.PrepareDream(DefaultConfig().Dream, true); err != nil || result.Outcome != "nothing_to_consolidate" {
		t.Fatalf("prepare dream=%#v err=%v", result, err)
	}
	if result, err := store.CommitDream("## Durable\n\nDo not write.", DreamInput{Eligible: 1}, 60); err != nil || result.Outcome != "nothing_to_consolidate" {
		t.Fatalf("dream=%#v err=%v", result, err)
	}
	if _, err := os.Stat(store.workspaceDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ephemeral operations created workspace directory: %v", err)
	}
}

func TestOpenWorkspaceDetectsTemporaryAndPersistentPaths(t *testing.T) {
	for _, path := range []string{"/tmp", "/tmp/worktree", "/var/tmp/worktree", "/private/tmp/worktree", "/var/folders/aa/bb/T", "/var/folders/aa/bb/T/worktree", "/private/var/folders/aa/bb/T/worktree"} {
		if !ephemeralWorkspace(path) {
			t.Errorf("temporary path not detected: %s", path)
		}
	}
	for _, path := range []string{"/home/user/project", "/Users/dev/project", "/opt/workspace", "/home/var/folders/aa/bb/T/project"} {
		if ephemeralWorkspace(path) {
			t.Errorf("persistent path misclassified: %s", path)
		}
	}
	store, err := OpenWorkspace(t.TempDir(), "/home/user/project", "persistent")
	if err != nil || store.IsEphemeral() {
		t.Fatalf("store=%#v err=%v", store, err)
	}
	if _, err := os.Stat(store.sessionsDir); err != nil {
		t.Fatalf("persistent sessions missing: %v", err)
	}
	flat, err := Open(t.TempDir(), t.TempDir(), "flat-memory")
	if err != nil || flat.IsEphemeral() {
		t.Fatalf("flat store=%#v err=%v", flat, err)
	}
}
