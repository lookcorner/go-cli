package memory

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenIndexCreatesSchema(t *testing.T) {
	dir := t.TempDir()
	idx, err := OpenIndex(dir, EmbeddingConfig{Provider: "api", Dimensions: 1024})
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
