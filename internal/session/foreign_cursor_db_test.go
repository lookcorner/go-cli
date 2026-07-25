package session

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestForeignSummariesCursorDatabaseFindsCurrentWorkspace(t *testing.T) {
	root, cwd, other := t.TempDir(), canonicalTempDir(t), canonicalTempDir(t)
	t.Setenv("CURSOR_USER_DATA_DIR", root)
	now := time.Now()
	createCursorStateDB(t, root,
		cursorDBRow{id: "11111111-1111-4111-8111-111111111111", cwd: cwd, updated: now.Add(-time.Minute), name: "  Cursor   title  ", subtitle: "fallback", branch: "feature/cursor"},
		cursorDBRow{id: "22222222-2222-4222-8222-222222222222", cwd: other, updated: now, name: "other workspace"},
		cursorDBRow{id: "33333333-3333-4333-8333-333333333333", cwd: cwd, updated: now, name: "archived", archived: true},
		cursorDBRow{id: "44444444-4444-4444-8444-444444444444", cwd: cwd, updated: now, name: "subagent", subagent: true},
	)

	items := ForeignSummaries(cwd, ForeignSources{Cursor: true})
	if len(items) != 1 || items[0].ID != "11111111-1111-4111-8111-111111111111" || items[0].Title != "Cursor title" || items[0].Branch != "feature/cursor" || items[0].Source != "cursor" {
		t.Fatalf("items=%#v", items)
	}
	if disabled := ForeignSummaries(cwd, ForeignSources{}); len(disabled) != 0 {
		t.Fatalf("disabled=%#v", disabled)
	}
}

func TestForeignSummariesCursorDatabaseUsesSubtitleAndCreatedTime(t *testing.T) {
	root, cwd := t.TempDir(), canonicalTempDir(t)
	t.Setenv("CURSOR_USER_DATA_DIR", root)
	now := time.Now().Add(-2 * time.Minute)
	createCursorStateDB(t, root, cursorDBRow{
		id: "55555555-5555-4555-8555-555555555555", cwd: cwd, created: now, subtitle: "subtitle fallback", nullUpdated: true,
	})

	items := ForeignSummaries(cwd, ForeignSources{Cursor: true})
	if len(items) != 1 || items[0].Title != "subtitle fallback" || items[0].UpdatedAt.Sub(now.UTC()).Abs() > time.Millisecond {
		t.Fatalf("items=%#v", items)
	}
}

func TestForeignSummariesCursorDatabaseRejectsUnsafeAndMalformedData(t *testing.T) {
	t.Run("symlinked global storage", func(t *testing.T) {
		root, outside, cwd := t.TempDir(), t.TempDir(), canonicalTempDir(t)
		t.Setenv("CURSOR_USER_DATA_DIR", root)
		createCursorStateDB(t, outside, cursorDBRow{id: "66666666-6666-4666-8666-666666666666", cwd: cwd, updated: time.Now(), name: "unsafe"})
		if err := os.Symlink(filepath.Join(outside, "globalStorage"), filepath.Join(root, "globalStorage")); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		if items := ForeignSummaries(cwd, ForeignSources{Cursor: true}); len(items) != 0 {
			t.Fatalf("items=%#v", items)
		}
	})

	t.Run("bad rows", func(t *testing.T) {
		root, cwd := t.TempDir(), canonicalTempDir(t)
		t.Setenv("CURSOR_USER_DATA_DIR", root)
		createCursorStateDB(t, root,
			cursorDBRow{id: "not-a-uuid", cwd: cwd, updated: time.Now(), name: "bad id"},
			cursorDBRow{id: "77777777-7777-4777-8777-777777777777", cwd: cwd, updated: time.Now().Add(-31 * 24 * time.Hour), name: "old"},
			cursorDBRow{id: "88888888-8888-4888-8888-888888888888", cwd: cwd, updated: time.Now(), name: strings.Repeat("x", cursorMaxHeaderBytes)},
			cursorDBRow{id: "99999999-9999-4999-8999-999999999999", cwd: cwd, updated: time.Now(), raw: "{"},
		)
		if items := ForeignSummaries(cwd, ForeignSources{Cursor: true}); len(items) != 0 {
			t.Fatalf("items=%#v", items)
		}
	})
}

type cursorDBRow struct {
	id, cwd, name, subtitle, branch, raw string
	created, updated                     time.Time
	archived, subagent, nullUpdated      bool
}

func createCursorStateDB(t *testing.T, root string, rows ...cursorDBRow) {
	t.Helper()
	path := filepath.Join(root, "globalStorage", "state.vscdb")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err = database.Exec(`CREATE TABLE composerHeaders (
		composerId TEXT PRIMARY KEY, workspaceId TEXT, createdAt INTEGER, lastUpdatedAt INTEGER,
		isArchived INTEGER, isSubagent INTEGER, recency INTEGER, checkpointAt INTEGER, value TEXT
	)`); err != nil {
		t.Fatal(err)
	}
	for index, row := range rows {
		created := row.created
		if created.IsZero() {
			created = row.updated
		}
		updated := any(row.updated.UnixMilli())
		if row.nullUpdated {
			updated = nil
		}
		header := map[string]any{
			"composerId": row.id, "name": row.name, "subtitle": row.subtitle,
			"workspaceIdentifier": map[string]any{"uri": map[string]any{"fsPath": row.cwd, "path": row.cwd}},
			"trackedGitRepos":     []map[string]any{{"repoPath": row.cwd, "branchName": row.branch}},
		}
		raw := []byte(row.raw)
		if len(raw) == 0 {
			var err error
			raw, err = json.Marshal(header)
			if err != nil {
				t.Fatal(err)
			}
		}
		archived, subagent := 0, 0
		if row.archived {
			archived = 1
		}
		if row.subagent {
			subagent = 1
		}
		if _, err = database.Exec(`INSERT INTO composerHeaders
			(composerId, workspaceId, createdAt, lastUpdatedAt, isArchived, isSubagent, recency, value)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, row.id, fmt.Sprintf("workspace-%d", index), created.UnixMilli(), updated, archived, subagent, index, string(raw)); err != nil {
			t.Fatal(err)
		}
	}
}
