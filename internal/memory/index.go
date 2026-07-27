package memory

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	_ "modernc.org/sqlite"
)

// SchemaVersion matches Rust xai-grok-memory SCHEMA_VERSION.
const SchemaVersion = 1

// IndexDB is the durable SQLite memory index (meta + chunks + FTS5).
// Vector table creation is deferred until sqlite-vec is wired.
type IndexDB struct {
	db   *sql.DB
	path string
}

// SchemaSQL returns the Rust-compatible schema without chunks_vec.
func SchemaSQL() string {
	return `
CREATE TABLE IF NOT EXISTS meta (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS chunks (
    rowid INTEGER PRIMARY KEY AUTOINCREMENT,
    id TEXT UNIQUE NOT NULL,
    path TEXT NOT NULL,
    start_line INTEGER NOT NULL,
    end_line INTEGER NOT NULL,
    text TEXT NOT NULL,
    hash TEXT NOT NULL,
    source TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    access_count INTEGER DEFAULT 0,
    last_accessed INTEGER
);

CREATE INDEX IF NOT EXISTS idx_chunks_path ON chunks(path);
CREATE INDEX IF NOT EXISTS idx_chunks_hash ON chunks(hash);

CREATE VIRTUAL TABLE IF NOT EXISTS chunks_fts USING fts5(text, content='');

INSERT OR IGNORE INTO meta(key, value) VALUES ('reindex_claim', '');
`
}

// IndexPath returns the workspace memory index.sqlite path.
func IndexPath(workspaceMemoryDir string) string {
	return filepath.Join(workspaceMemoryDir, "index.sqlite")
}

// OpenIndex opens or creates the memory index under workspaceMemoryDir.
func OpenIndex(workspaceMemoryDir string, embedding EmbeddingConfig) (*IndexDB, error) {
	if err := os.MkdirAll(workspaceMemoryDir, 0o700); err != nil {
		return nil, err
	}
	path := IndexPath(workspaceMemoryDir)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec("PRAGMA busy_timeout = 5000"); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.Exec(SchemaSQL()); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("apply memory index schema: %w", err)
	}
	idx := &IndexDB{db: db, path: path}
	if err := idx.SetMeta("schema_version", strconv.Itoa(SchemaVersion)); err != nil {
		_ = db.Close()
		return nil, err
	}
	dims := embedding.Dimensions
	if dims <= 0 {
		dims = 1024
	}
	if err := idx.SetMeta("embedding_dimensions", strconv.Itoa(dims)); err != nil {
		_ = db.Close()
		return nil, err
	}
	model := ""
	if embedding.Model != nil {
		model = *embedding.Model
	}
	if err := idx.SetMeta("embedding_model", model); err != nil {
		_ = db.Close()
		return nil, err
	}
	return idx, nil
}

func (i *IndexDB) Path() string {
	if i == nil {
		return ""
	}
	return i.path
}

func (i *IndexDB) Close() error {
	if i == nil || i.db == nil {
		return nil
	}
	return i.db.Close()
}

func (i *IndexDB) SetMeta(key, value string) error {
	_, err := i.db.Exec(`INSERT OR REPLACE INTO meta(key, value) VALUES (?, ?)`, key, value)
	return err
}

func (i *IndexDB) GetMeta(key string) (string, bool, error) {
	var value string
	err := i.db.QueryRow(`SELECT value FROM meta WHERE key = ?`, key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return value, true, nil
}

// VectorSearchEnabled reports whether an embedding model is configured.
// Live KNN remains pending; callers should fail open to text search.
func VectorSearchEnabled(cfg Config) bool {
	return cfg.Embedding.Model != nil && strings.TrimSpace(*cfg.Embedding.Model) != ""
}
