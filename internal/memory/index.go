package memory

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// SchemaVersion matches Rust xai-grok-memory SCHEMA_VERSION.
const SchemaVersion = 1

// IndexDB is the durable SQLite memory index (meta + chunks + FTS5).
// Vector table creation is deferred until sqlite-vec is wired.
type IndexDB struct {
	db    *sql.DB
	path  string
	chunk IndexConfig
}

// FtsHit is one BM25 hit from chunks_fts.
type FtsHit struct {
	ChunkID   string
	Path      string
	StartLine int
	EndLine   int
	Text      string
	Source    string
	Rank      float64
	CreatedAt int64
}

// ReindexResult counts chunk mutations for one file.
type ReindexResult struct {
	Added   int
	Updated int
	Deleted int
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
func OpenIndex(workspaceMemoryDir string, embedding EmbeddingConfig, index IndexConfig) (*IndexDB, error) {
	if index.MaxChunkChars < 1 {
		index = DefaultConfig().Index
	}
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
	idx := &IndexDB{db: db, path: path, chunk: index}
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

// ChunkHash returns a stable hex digest for chunk text (sha256; Rust uses blake3).
func ChunkHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

type storedChunk struct {
	rowid int64
	id    string
	hash  string
	text  string
}

func (i *IndexDB) chunksForPath(path string) (map[string]storedChunk, error) {
	rows, err := i.db.Query(`SELECT rowid, id, hash, text FROM chunks WHERE path = ?`, path)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]storedChunk{}
	for rows.Next() {
		var c storedChunk
		if err := rows.Scan(&c.rowid, &c.id, &c.hash, &c.text); err != nil {
			return nil, err
		}
		out[c.id] = c
	}
	return out, rows.Err()
}

// ReindexFile upserts Markdown chunks for path into chunks + chunks_fts.
func (i *IndexDB) ReindexFile(path, source, content string) (ReindexResult, error) {
	if i == nil {
		return ReindexResult{}, fmt.Errorf("nil index")
	}
	path = filepath.Clean(path)
	chunks := splitMarkdown(path, source, content, 0, i.chunk)
	existing, err := i.chunksForPath(path)
	if err != nil {
		return ReindexResult{}, err
	}
	now := time.Now().Unix()
	tx, err := i.db.Begin()
	if err != nil {
		return ReindexResult{}, err
	}
	defer func() { _ = tx.Rollback() }()

	var result ReindexResult
	seen := map[string]bool{}
	for index, chunk := range chunks {
		chunkID := fmt.Sprintf("%s:%d", path, index)
		hash := ChunkHash(chunk.text)
		seen[chunkID] = true
		old, ok := existing[chunkID]
		switch {
		case ok && old.hash == hash:
			continue
		case ok:
			if _, err := tx.Exec(
				`UPDATE chunks SET text = ?, hash = ?, start_line = ?, end_line = ?, source = ?, updated_at = ? WHERE id = ?`,
				chunk.text, hash, chunk.start, chunk.end, source, now, chunkID,
			); err != nil {
				return ReindexResult{}, err
			}
			if _, err := tx.Exec(`INSERT INTO chunks_fts(chunks_fts, rowid, text) VALUES('delete', ?, ?)`, old.rowid, old.text); err != nil {
				return ReindexResult{}, err
			}
			var rowid int64
			if err := tx.QueryRow(`SELECT rowid FROM chunks WHERE id = ?`, chunkID).Scan(&rowid); err != nil {
				return ReindexResult{}, err
			}
			if _, err := tx.Exec(`INSERT INTO chunks_fts(rowid, text) VALUES (?, ?)`, rowid, chunk.text); err != nil {
				return ReindexResult{}, err
			}
			result.Updated++
		default:
			res, err := tx.Exec(
				`INSERT INTO chunks (id, path, start_line, end_line, text, hash, source, created_at, updated_at)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				chunkID, path, chunk.start, chunk.end, chunk.text, hash, source, now, now,
			)
			if err != nil {
				return ReindexResult{}, err
			}
			rowid, err := res.LastInsertId()
			if err != nil {
				return ReindexResult{}, err
			}
			if _, err := tx.Exec(`INSERT INTO chunks_fts(rowid, text) VALUES (?, ?)`, rowid, chunk.text); err != nil {
				return ReindexResult{}, err
			}
			result.Added++
		}
	}
	for id, old := range existing {
		if seen[id] {
			continue
		}
		if _, err := tx.Exec(`INSERT INTO chunks_fts(chunks_fts, rowid, text) VALUES('delete', ?, ?)`, old.rowid, old.text); err != nil {
			return ReindexResult{}, err
		}
		if _, err := tx.Exec(`DELETE FROM chunks WHERE id = ?`, id); err != nil {
			return ReindexResult{}, err
		}
		result.Deleted++
	}
	if err := tx.Commit(); err != nil {
		return ReindexResult{}, err
	}
	return result, nil
}

// SearchFTS runs BM25 keyword search over chunks_fts (Rust search_fts shape).
func (i *IndexDB) SearchFTS(query string, limit int) ([]FtsHit, error) {
	if i == nil {
		return nil, fmt.Errorf("nil index")
	}
	if limit < 1 {
		limit = 10
	}
	terms := tokens(query)
	if len(terms) == 0 {
		return nil, nil
	}
	quoted := make([]string, 0, len(terms))
	for _, term := range terms {
		quoted = append(quoted, quoteFTSTerm(term))
	}
	ftsQuery := strings.Join(quoted, " OR ")
	rows, err := i.db.Query(
		`SELECT c.id, c.path, c.start_line, c.end_line, c.text, c.source, f.rank, c.created_at
		 FROM chunks_fts f
		 JOIN chunks c ON c.rowid = f.rowid
		 WHERE chunks_fts MATCH ?
		 ORDER BY f.rank
		 LIMIT ?`,
		ftsQuery, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var hits []FtsHit
	for rows.Next() {
		var hit FtsHit
		if err := rows.Scan(&hit.ChunkID, &hit.Path, &hit.StartLine, &hit.EndLine, &hit.Text, &hit.Source, &hit.Rank, &hit.CreatedAt); err != nil {
			return nil, err
		}
		hits = append(hits, hit)
	}
	return hits, rows.Err()
}

func quoteFTSTerm(term string) string {
	term = strings.ReplaceAll(term, `"`, `""`)
	return `"` + term + `"`
}

// VectorSearchEnabled reports whether an embedding model is configured.
// Live KNN remains pending; callers should fail open to text search.
func VectorSearchEnabled(cfg Config) bool {
	return cfg.Embedding.Model != nil && strings.TrimSpace(*cfg.Embedding.Model) != ""
}
