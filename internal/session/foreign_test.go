package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestForeignSummariesFindsCurrentWorkspaceClaudeAndCodexSessions(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	cwd, _ = filepath.EvalSymlinks(cwd)
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	claudeID := "11111111-1111-4111-8111-111111111111"
	claudeDir := filepath.Join(home, ".claude", "projects", sanitizeClaudeProject(cwd))
	writeForeignFile(t, filepath.Join(claudeDir, claudeID+".jsonl"),
		map[string]any{"type": "user", "cwd": cwd, "message": map[string]any{"content": "Claude first prompt"}},
		map[string]any{"customTitle": "  Claude   title  ", "gitBranch": "feature/claude"},
	)
	codexID := "22222222-2222-4222-8222-222222222222"
	date := time.Now().Format("2006/01/02")
	writeForeignFile(t, filepath.Join(home, ".codex", "sessions", filepath.FromSlash(date), "rollout-2026-01-02T03-04-05-"+codexID+".jsonl"),
		map[string]any{"type": "session_meta", "payload": map[string]any{"id": codexID, "cwd": cwd, "source": "cli", "git": map[string]any{"branch": "feature/codex"}}},
		map[string]any{"type": "event_msg", "payload": map[string]any{"type": "user_message", "message": "Codex first prompt"}},
	)

	items := ForeignSummaries(cwd, ForeignSources{Claude: true, Codex: true})
	if len(items) != 2 {
		t.Fatalf("items=%#v", items)
	}
	bySource := map[string]ForeignSummary{items[0].Source: items[0], items[1].Source: items[1]}
	if item := bySource["claude"]; item.ID != claudeID || item.Title != "Claude title" || item.Branch != "feature/claude" || item.CWD != cwd {
		t.Fatalf("claude=%#v", item)
	}
	if item := bySource["codex"]; item.ID != codexID || item.Title != "Codex first prompt" || item.Branch != "feature/codex" || item.CWD != cwd {
		t.Fatalf("codex=%#v", item)
	}
}

func TestForeignSummariesHonorsSourceWorkspaceAgeAndSafetyBoundaries(t *testing.T) {
	home, cwd, other := t.TempDir(), t.TempDir(), t.TempDir()
	cwd, _ = filepath.EvalSymlinks(cwd)
	other, _ = filepath.EvalSymlinks(other)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	project := filepath.Join(home, ".claude", "projects", sanitizeClaudeProject(cwd))
	valid := "33333333-3333-4333-8333-333333333333"
	writeForeignFile(t, filepath.Join(project, valid+".jsonl"), map[string]any{"type": "user", "cwd": cwd, "message": map[string]any{"content": "valid"}})
	writeForeignFile(t, filepath.Join(project, "44444444-4444-4444-8444-444444444444.jsonl"), map[string]any{"type": "user", "cwd": other, "message": map[string]any{"content": "other"}})
	old := filepath.Join(project, "55555555-5555-4555-8555-555555555555.jsonl")
	writeForeignFile(t, old, map[string]any{"type": "user", "cwd": cwd, "message": map[string]any{"content": "old"}})
	oldTime := time.Now().Add(-31 * 24 * time.Hour)
	if err := os.Chtimes(old, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	sidechain := filepath.Join(project, "66666666-6666-4666-8666-666666666666.jsonl")
	writeForeignFile(t, sidechain, map[string]any{"type": "user", "cwd": cwd, "isSidechain": true, "message": map[string]any{"content": "sidechain"}})

	items := ForeignSummaries(cwd, ForeignSources{Claude: true})
	if len(items) != 1 || items[0].ID != valid {
		t.Fatalf("items=%#v", items)
	}
	if disabled := ForeignSummaries(cwd, ForeignSources{}); len(disabled) != 0 {
		t.Fatalf("disabled=%#v", disabled)
	}
}

func TestForeignSummariesRejectsOverlongClaudeProjectKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	root := filepath.Join(home, ".claude", "projects")
	cwd := "/" + strings.Repeat("a", 210)
	writeForeignFile(t, filepath.Join(root, "77777777-7777-4777-8777-777777777777.jsonl"), map[string]any{"type": "user", "cwd": cwd, "message": map[string]any{"content": "wrong root"}})
	if items := ForeignSummaries(cwd, ForeignSources{Claude: true}); len(items) != 0 {
		t.Fatalf("items=%#v", items)
	}
}

func writeForeignFile(t *testing.T, path string, records ...map[string]any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	encoder := json.NewEncoder(file)
	for _, record := range records {
		if err := encoder.Encode(record); err != nil {
			t.Fatal(err)
		}
	}
}
