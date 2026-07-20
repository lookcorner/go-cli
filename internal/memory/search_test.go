package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSplitMarkdownUsesHeadersOverlapAndBoundedChunks(t *testing.T) {
	text := "# Root\n\nintro text\n\n## Child\n\n" + strings.Repeat("alpha beta gamma\n", 20)
	chunks := splitMarkdown("memory.md", "workspace", text, 0, IndexConfig{MaxChunkChars: 100, ChunkOverlapChars: 20})
	if len(chunks) < 3 {
		t.Fatalf("chunks=%#v", chunks)
	}
	for _, item := range chunks {
		if runeLen(item.text) > 100 {
			t.Fatalf("oversized chunk (%d): %q", runeLen(item.text), item.text)
		}
		if item.start < 0 || item.end <= item.start {
			t.Fatalf("invalid range: %#v", item)
		}
	}
	if !strings.Contains(chunks[1].text, "[Context: # Root]") {
		t.Fatalf("missing ancestor context: %#v", chunks)
	}

	long := splitMarkdown("memory.md", "workspace", strings.Repeat("x", 250), 0, IndexConfig{MaxChunkChars: 100, ChunkOverlapChars: 20})
	if len(long) != 3 || runeLen(long[0].text) != 100 || !strings.HasSuffix(long[0].text, strings.Repeat("x", 20)) || !strings.HasPrefix(long[1].text, strings.Repeat("x", 20)) {
		t.Fatalf("long-line chunks=%#v", long)
	}
	if got := splitMarkdown("memory.md", "workspace", "# Notes\n\n## Decisions\n", 0, IndexConfig{MaxChunkChars: 20, ChunkOverlapChars: 5}); len(got) != 0 {
		t.Fatalf("scaffold chunks=%#v", got)
	}
}

func TestStoreSearchRanksFiltersAndDecaysSessions(t *testing.T) {
	root, workspace := t.TempDir(), t.TempDir()
	store, err := Open(root, workspace, "search")
	if err != nil {
		t.Fatal(err)
	}
	global := filepath.Join(root, "MEMORY.md")
	workspaceFile := filepath.Join(store.workspaceDir, "MEMORY.md")
	if err := os.WriteFile(global, []byte("global deployment convention\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(workspaceFile, []byte("deployment deployment rollback procedure\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	session, _, err := store.Write("manual", "deployment incident notes")
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(session, old, old); err != nil {
		t.Fatal(err)
	}

	results, err := store.Search("deployment rollback", IndexConfig{MaxChunkChars: 1600, ChunkOverlapChars: 320}, SearchConfig{MaxResults: 2, MinScore: 0.1})
	if err != nil {
		t.Fatal(err)
	}
	canonicalWorkspaceFile, err := filepath.EvalSymlinks(workspaceFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].Path != canonicalWorkspaceFile || results[0].Score != 1 || results[1].Source != "global" {
		t.Fatalf("results=%#v", results)
	}
	if empty, err := store.Search("not-present", DefaultConfig().Index, DefaultConfig().Search); err != nil || len(empty) != 0 {
		t.Fatalf("empty=%#v err=%v", empty, err)
	}
}

func TestStoreGetAllowsOnlyActiveMemoryFilesAndPreservesTrailingLine(t *testing.T) {
	store, err := Open(t.TempDir(), t.TempDir(), "get")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store.workspaceDir, "MEMORY.md")
	if err := os.WriteFile(path, []byte("one\ntwo\nthree\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := store.Get(path, 1, 3)
	if err != nil || len(file.Lines) != 3 || file.Lines[0] != "two" || file.Lines[2] != "" {
		t.Fatalf("file=%#v err=%v", file, err)
	}
	outside := filepath.Join(t.TempDir(), "MEMORY.md")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(outside, 0, 0); err == nil {
		t.Fatal("outside path was readable")
	}
	link := filepath.Join(store.sessionsDir, "link.md")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(link, 0, 0); err == nil {
		t.Fatal("symlink was readable")
	}
	moved := store.sessionsDir + "-real"
	if err := os.Rename(store.sessionsDir, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Dir(outside), store.sessionsDir); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(filepath.Join(store.sessionsDir, filepath.Base(outside)), 0, 0); err == nil {
		t.Fatal("replaced sessions directory escaped")
	}
}
