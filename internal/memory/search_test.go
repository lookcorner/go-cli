package memory

import (
	"math"
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

	search := DefaultConfig().Search
	search.MaxResults, search.MinScore = 2, 0.1
	results, err := store.Search("deployment rollback", DefaultConfig().Index, search)
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

func TestRankChunksUsesTemporalDecaySourceWeightsAndMMR(t *testing.T) {
	now := time.Now().Unix()
	chunks := []chunk{
		{path: "a.md", source: "session", text: "rust async programming patterns", start: 0, end: 1, created: now - 7*86400},
		{path: "b.md", source: "workspace", text: "rust async programming tutorial", start: 0, end: 1, created: now - 100*86400},
		{path: "c.md", source: "global", text: "rust python web framework flask", start: 0, end: 1, created: now - 100*86400},
	}
	cfg := DefaultConfig().Search
	cfg.MaxResults = 3
	cfg.MinScore = 0
	cfg.SourceWeights["global"] = 0.9
	results := rankChunks(chunks, tokens("rust async programming"), cfg)
	if len(results) != 3 || results[0].Path != "b.md" || results[1].Path != "a.md" || math.Abs(results[1].Score-0.5) > 0.01 {
		t.Fatalf("decayed results=%#v", results)
	}

	cfg.MMR.Enabled = true
	cfg.MMR.Lambda = 0.5
	results = rankChunks(chunks, tokens("rust async programming"), cfg)
	if results[0].Path != "b.md" || results[1].Path != "c.md" || results[2].Path != "a.md" {
		t.Fatalf("MMR results=%#v", results)
	}

	cfg.TemporalDecay.Enabled = false
	cfg.RecencyDecay = 0.5
	if halfLife := effectiveHalfLife(cfg); math.Abs(halfLife-1) > 1e-9 {
		t.Fatalf("legacy half-life=%v", halfLife)
	}
	cfg.RecencyDecay = defaultRecencyDecay
	if halfLife := effectiveHalfLife(cfg); halfLife != 0 {
		t.Fatalf("disabled half-life=%v", halfLife)
	}
	cfg.TemporalDecay.Enabled = true
	cfg.TemporalDecay.HalfLifeDays = -1
	if halfLife := effectiveHalfLife(cfg); halfLife != 0 {
		t.Fatalf("invalid half-life=%v", halfLife)
	}
}

func TestStoreGetAllowsOnlyActiveMemoryFilesAndMatchesOptionalRanges(t *testing.T) {
	store, err := Open(t.TempDir(), t.TempDir(), "get")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store.workspaceDir, "MEMORY.md")
	if err := os.WriteFile(path, []byte("one\ntwo\nthree\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	three, zero := 3, 0
	content, err := store.Get(path, 1, &three)
	if err != nil || content != "two\nthree" {
		t.Fatalf("content=%q err=%v", content, err)
	}
	if content, err = store.Get(path, 0, nil); err != nil || content != "one\ntwo\nthree\n" {
		t.Fatalf("full content=%q err=%v", content, err)
	}
	if content, err = store.Get(path, 0, &zero); err != nil || content != "" {
		t.Fatalf("zero range content=%q err=%v", content, err)
	}
	huge := int(^uint(0) >> 1)
	if content, err = store.Get(path, 1, &huge); err != nil || content != "two\nthree" {
		t.Fatalf("huge range content=%q err=%v", content, err)
	}
	outside := filepath.Join(t.TempDir(), "MEMORY.md")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(outside, 0, nil); err == nil {
		t.Fatal("outside path was readable")
	}
	link := filepath.Join(store.sessionsDir, "link.md")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(link, 0, nil); err == nil {
		t.Fatal("symlink was readable")
	}
	moved := store.sessionsDir + "-real"
	if err := os.Rename(store.sessionsDir, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Dir(outside), store.sessionsDir); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(filepath.Join(store.sessionsDir, filepath.Base(outside)), 0, nil); err == nil {
		t.Fatal("replaced sessions directory escaped")
	}
}

func TestResultStalenessNoteMatchesReferenceAgeLabels(t *testing.T) {
	now := time.Unix(2_000_000_000, 0)
	for _, test := range []struct {
		name   string
		result Result
		want   string
	}{
		{name: "global evergreen", result: Result{Source: "global", CreatedAt: now.Add(-30 * 24 * time.Hour).Unix()}},
		{name: "workspace evergreen", result: Result{Source: "workspace", CreatedAt: now.Add(-30 * 24 * time.Hour).Unix()}},
		{name: "unknown age", result: Result{Source: "session"}},
		{name: "fresh", result: Result{Source: "session", CreatedAt: now.Add(-12 * time.Hour).Unix()}},
		{name: "moderate", result: Result{Source: "session", CreatedAt: now.Add(-48 * time.Hour).Unix()}, want: "**Note (2 days ago):** Verify this is still current."},
		{name: "very stale", result: Result{Source: "session", CreatedAt: now.Add(-10 * 24 * time.Hour).Unix()}, want: "**Stale (1 week ago):** Verify current state before relying on this."},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := test.result.StalenessNote(now); got != test.want {
				t.Fatalf("note=%q want=%q", got, test.want)
			}
		})
	}
}
