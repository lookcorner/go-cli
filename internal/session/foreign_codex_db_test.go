package session

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestForeignSummariesUsesHighestCodexStateDatabase(t *testing.T) {
	home, cwd := t.TempDir(), canonicalTempDir(t)
	t.Setenv("CODEX_HOME", home)
	now := time.Now()
	lowerID := "10000000-0000-4000-8000-000000000001"
	higherID := "20000000-0000-4000-8000-000000000002"
	lowerRollout := writeCodexRollout(t, home, lowerID)
	higherRollout := writeCodexRollout(t, home, higherID)
	createCodexStateDB(t, filepath.Join(home, "state_4.sqlite"), true, codexDBRow{
		id: lowerID, rollout: lowerRollout, updated: now.Add(-time.Minute), source: "cli", cwd: cwd, title: "lower database",
	})
	createCodexStateDB(t, filepath.Join(home, "state_5.sqlite"), true, codexDBRow{
		id: higherID, rollout: higherRollout, updated: now.Add(-2 * time.Minute), source: "vscode", cwd: cwd, title: "higher database", branch: "feature/sqlite",
	})

	items := ForeignSummaries(cwd, ForeignSources{Codex: true})
	if len(items) != 1 || items[0].ID != higherID || items[0].Title != "higher database" || items[0].Branch != "feature/sqlite" {
		t.Fatalf("items=%#v", items)
	}
}

func TestForeignSummariesCodexDatabaseSupportsLegacySecondsAndFallbackTitle(t *testing.T) {
	home, cwd := t.TempDir(), canonicalTempDir(t)
	t.Setenv("CODEX_HOME", home)
	id := "30000000-0000-4000-8000-000000000003"
	rollout := writeCodexRollout(t, home, id)
	createCodexStateDB(t, filepath.Join(home, "state_3.sqlite"), false, codexDBRow{
		id: id, rollout: rollout, updated: time.Now().Add(-3 * time.Minute), source: `{"custom":"atlas"}`, cwd: cwd, first: "fallback prompt",
	})

	items := ForeignSummaries(cwd, ForeignSources{Codex: true})
	if len(items) != 1 || items[0].ID != id || items[0].Title != "fallback prompt" {
		t.Fatalf("items=%#v", items)
	}
}

func TestForeignSummariesCodexDatabaseFiltersUnsafeRows(t *testing.T) {
	home, cwd := t.TempDir(), canonicalTempDir(t)
	other := canonicalTempDir(t)
	t.Setenv("CODEX_HOME", home)
	now := time.Now()
	validID := "40000000-0000-4000-8000-000000000004"
	validRollout := writeCodexRollout(t, home, validID)
	outsideID := "50000000-0000-4000-8000-000000000005"
	outside := filepath.Join(t.TempDir(), "rollout-2026-01-02T03-04-05-"+outsideID+".jsonl")
	if err := os.WriteFile(outside, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	createCodexStateDB(t, filepath.Join(home, "state_5.sqlite"), true,
		codexDBRow{id: validID, rollout: validRollout, updated: now, source: "cli", cwd: cwd, title: "valid"},
		codexDBRow{id: "not-a-uuid", rollout: validRollout, updated: now, source: "cli", cwd: cwd, title: "bad id"},
		codexDBRow{id: outsideID, rollout: outside, updated: now, source: "cli", cwd: cwd, title: "outside"},
		codexDBRow{id: "60000000-0000-4000-8000-000000000006", rollout: validRollout, updated: now, source: "subagent", cwd: cwd, title: "bad source"},
		codexDBRow{id: "70000000-0000-4000-8000-000000000007", rollout: validRollout, updated: now, source: "cli", cwd: other, title: "other cwd"},
		codexDBRow{id: "80000000-0000-4000-8000-000000000008", rollout: validRollout, updated: now, source: "cli", cwd: cwd, title: "archived", archived: true},
	)

	items := ForeignSummaries(cwd, ForeignSources{Codex: true})
	if len(items) != 1 || items[0].ID != validID {
		t.Fatalf("items=%#v", items)
	}
}

func TestForeignSummariesCodexDatabaseFallsBackToRolloutFiles(t *testing.T) {
	home, cwd := t.TempDir(), canonicalTempDir(t)
	t.Setenv("CODEX_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "state_5.sqlite"), []byte("not sqlite"), 0o600); err != nil {
		t.Fatal(err)
	}
	id := "90000000-0000-4000-8000-000000000009"
	date := time.Now().Format("2006/01/02")
	writeForeignFile(t, filepath.Join(home, "sessions", filepath.FromSlash(date), "rollout-2026-01-02T03-04-05-"+id+".jsonl"),
		map[string]any{"type": "session_meta", "payload": map[string]any{"id": id, "cwd": cwd, "source": "cli"}},
		map[string]any{"type": "event_msg", "payload": map[string]any{"type": "user_message", "message": "file fallback"}},
	)

	items := ForeignSummaries(cwd, ForeignSources{Codex: true})
	if len(items) != 1 || items[0].ID != id || items[0].Title != "file fallback" {
		t.Fatalf("items=%#v", items)
	}
}

func TestForeignSummariesCodexDatabaseRejectsSymlinkedSessionRoot(t *testing.T) {
	home, cwd, outside := t.TempDir(), canonicalTempDir(t), t.TempDir()
	t.Setenv("CODEX_HOME", home)
	if err := os.Symlink(outside, filepath.Join(home, "sessions")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	id := "a0000000-0000-4000-8000-00000000000a"
	rollout := filepath.Join(outside, "2026", "01", "02", "rollout-2026-01-02T03-04-05-"+id+".jsonl")
	if err := os.MkdirAll(filepath.Dir(rollout), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rollout, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	createCodexStateDB(t, filepath.Join(home, "state_5.sqlite"), true, codexDBRow{
		id: id, rollout: rollout, updated: time.Now(), source: "cli", cwd: cwd, title: "outside",
	})

	if items := ForeignSummaries(cwd, ForeignSources{Codex: true}); len(items) != 0 {
		t.Fatalf("items=%#v", items)
	}
}

type codexDBRow struct {
	id, rollout, source, cwd, title, first, branch string
	updated                                        time.Time
	archived                                       bool
}

func createCodexStateDB(t *testing.T, path string, millis bool, rows ...codexDBRow) {
	t.Helper()
	updatedColumn := "updated_at"
	if millis {
		updatedColumn = "updated_at_ms"
	}
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err = database.Exec(fmt.Sprintf(`CREATE TABLE threads (
		id TEXT, rollout_path TEXT, %s INTEGER, source TEXT, cwd TEXT,
		title TEXT, first_user_message TEXT, archived INTEGER, git_branch TEXT
	)`, updatedColumn)); err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		updated := row.updated.Unix()
		if millis {
			updated = row.updated.UnixMilli()
		}
		archived := 0
		if row.archived {
			archived = 1
		}
		if _, err = database.Exec(fmt.Sprintf("INSERT INTO threads (id, rollout_path, %s, source, cwd, title, first_user_message, archived, git_branch) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)", updatedColumn),
			row.id, row.rollout, updated, row.source, row.cwd, row.title, row.first, archived, row.branch); err != nil {
			t.Fatal(err)
		}
	}
}

func writeCodexRollout(t *testing.T, home, id string) string {
	t.Helper()
	path := filepath.Join(home, "sessions", "2026", "01", "02", "rollout-2026-01-02T03-04-05-"+id+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func canonicalTempDir(t *testing.T) string {
	t.Helper()
	path, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return path
}
