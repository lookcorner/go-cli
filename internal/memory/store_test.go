package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
}
