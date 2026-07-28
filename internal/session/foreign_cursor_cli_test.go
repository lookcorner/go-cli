package session

import (
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestForeignSummariesCursorCLIFindsCurrentWorkspace(t *testing.T) {
	home, cwd, other := t.TempDir(), canonicalTempDir(t), canonicalTempDir(t)
	t.Setenv("HOME", home)
	t.Setenv("CURSOR_HOME", filepath.Join(home, ".cursor"))
	t.Setenv("CURSOR_USER_DATA_DIR", filepath.Join(home, "missing-desktop"))
	now := time.Now()
	createCursorCLIStore(t, home, cwd, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "CLI title", now.Add(-time.Minute))
	createCursorCLIStore(t, home, other, "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", "other workspace", now)

	items := ForeignSummaries(cwd, ForeignSources{Cursor: true})
	if len(items) != 1 || items[0].ID != "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa" || items[0].Title != "CLI title" || items[0].Source != "cursor" {
		t.Fatalf("items=%#v", items)
	}
	if hash := cursorCLIWorkspaceHash(cwd); hash == cursorCLIWorkspaceHash(other) {
		t.Fatal("workspace hashes collided")
	}
}

func TestForeignSummariesCursorMergesDesktopAndCLI(t *testing.T) {
	home, cwd := t.TempDir(), canonicalTempDir(t)
	desktopRoot := filepath.Join(home, "desktop-user")
	t.Setenv("HOME", home)
	t.Setenv("CURSOR_HOME", filepath.Join(home, ".cursor"))
	t.Setenv("CURSOR_USER_DATA_DIR", desktopRoot)
	now := time.Now()
	createCursorStateDB(t, desktopRoot, cursorDBRow{
		id: "11111111-1111-4111-8111-111111111111", cwd: cwd, updated: now.Add(-2 * time.Minute), name: "Desktop title",
	})
	createCursorCLIStore(t, home, cwd, "cccccccc-cccc-4ccc-8ccc-cccccccccccc", "CLI only", now.Add(-time.Minute))

	items := ForeignSummaries(cwd, ForeignSources{Cursor: true})
	if len(items) != 2 {
		t.Fatalf("items=%#v", items)
	}
	byID := map[string]ForeignSummary{}
	for _, item := range items {
		byID[item.ID] = item
	}
	if byID["11111111-1111-4111-8111-111111111111"].Title != "Desktop title" || byID["cccccccc-cccc-4ccc-8ccc-cccccccccccc"].Title != "CLI only" {
		t.Fatalf("items=%#v", items)
	}
}

func TestForeignSummariesCursorCLIRejectsSymlinkedStore(t *testing.T) {
	home, cwd, outside := t.TempDir(), canonicalTempDir(t), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CURSOR_HOME", filepath.Join(home, ".cursor"))
	t.Setenv("CURSOR_USER_DATA_DIR", filepath.Join(home, "missing-desktop"))
	id := "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
	realDB := filepath.Join(outside, "store.db")
	writeCursorCLIStoreFile(t, realDB, id, "unsafe", time.Now())
	dir := filepath.Join(home, ".cursor", "chats", cursorCLIWorkspaceHash(cwd), id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realDB, filepath.Join(dir, "store.db")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if items := ForeignSummaries(cwd, ForeignSources{Cursor: true}); len(items) != 0 {
		t.Fatalf("items=%#v", items)
	}
}

func createCursorCLIStore(t *testing.T, home, cwd, id, name string, created time.Time) {
	t.Helper()
	dir := filepath.Join(home, ".cursor", "chats", cursorCLIWorkspaceHash(cwd), id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeCursorCLIStoreFile(t, filepath.Join(dir, "store.db"), id, name, created)
}

func writeCursorCLIStoreFile(t *testing.T, path, id, name string, created time.Time) {
	t.Helper()
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.Exec(`CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT);
CREATE TABLE blobs (id TEXT PRIMARY KEY, data BLOB);`); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(cursorCLIMeta{
		AgentID: id, Name: name, CreatedAt: created.UnixMilli(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO meta(key, value) VALUES('0', ?)`, hex.EncodeToString(payload)); err != nil {
		t.Fatal(err)
	}
}
