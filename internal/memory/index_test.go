package memory

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenIndexCreatesSchema(t *testing.T) {
	dir := t.TempDir()
	idx, err := OpenIndex(dir, EmbeddingConfig{Provider: "api", Dimensions: 1024}, DefaultConfig().Index)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	if idx.Path() != filepath.Join(dir, "index.sqlite") {
		t.Fatalf("path=%q", idx.Path())
	}
	version, ok, err := idx.GetMeta("schema_version")
	if err != nil || !ok || version != "1" {
		t.Fatalf("schema_version=%q ok=%v err=%v", version, ok, err)
	}
	dims, ok, err := idx.GetMeta("embedding_dimensions")
	if err != nil || !ok || dims != "1024" {
		t.Fatalf("dims=%q ok=%v err=%v", dims, ok, err)
	}
	model, ok, err := idx.GetMeta("embedding_model")
	if err != nil || !ok || model != "" {
		t.Fatalf("model=%q ok=%v err=%v", model, ok, err)
	}
	sql := SchemaSQL()
	if !strings.Contains(sql, "chunks_fts") || strings.Contains(sql, "chunks_vec") {
		t.Fatal("schema should include FTS and omit vec")
	}
}

func TestReindexFileAndSearchFTS(t *testing.T) {
	dir := t.TempDir()
	idx, err := OpenIndex(dir, EmbeddingConfig{Provider: "api", Dimensions: 1024}, IndexConfig{MaxChunkChars: 1600, ChunkOverlapChars: 320})
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()

	path := filepath.Join(dir, "MEMORY.md")
	content := "# Notes\n\nRust ownership and borrowing rules live here.\n\n## Other\n\nUnrelated pasta recipes.\n"
	got, err := idx.ReindexFile(path, "workspace", content)
	if err != nil {
		t.Fatal(err)
	}
	if got.Added < 1 {
		t.Fatalf("expected added chunks, got %+v", got)
	}

	hits, err := idx.SearchFTS("ownership borrowing", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("expected FTS hits for ownership")
	}
	if !strings.Contains(strings.ToLower(hits[0].Text), "ownership") {
		t.Fatalf("unexpected hit text: %q", hits[0].Text)
	}

	// Unchanged content should skip updates.
	again, err := idx.ReindexFile(path, "workspace", content)
	if err != nil {
		t.Fatal(err)
	}
	if again.Added != 0 || again.Updated != 0 || again.Deleted != 0 {
		t.Fatalf("unchanged reindex should be no-op: %+v", again)
	}

	// Shrinking file deletes stale chunks.
	shrunk, err := idx.ReindexFile(path, "workspace", "# Empty\n")
	if err != nil {
		t.Fatal(err)
	}
	if shrunk.Deleted < 1 && shrunk.Updated < 1 && shrunk.Added < 1 {
		t.Fatalf("expected mutations on shrink: %+v", shrunk)
	}
}

func TestVectorSearchEnabled(t *testing.T) {
	cfg := DefaultConfig()
	if VectorSearchEnabled(cfg) {
		t.Fatal("default should be text-only")
	}
	model := "text-embedding-3-small"
	cfg.Embedding.Model = &model
	if !VectorSearchEnabled(cfg) {
		t.Fatal("configured model should enable vector flag")
	}
}
