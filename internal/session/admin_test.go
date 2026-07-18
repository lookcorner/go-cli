package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSessionAdminRenameSearchAndDelete(t *testing.T) {
	dir, cwd := t.TempDir(), t.TempDir()
	first, err := NewLoggerWithID(dir, "session-one")
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Append("session_metadata", map[string]any{"cwd": cwd, "modelId": "model-one"}); err != nil {
		t.Fatal(err)
	}
	if err := first.Append("user_prompt", map[string]any{"text": "初始标题"}); err != nil {
		t.Fatal(err)
	}
	if err := first.Append("model_response", map[string]any{"text": "needle result", "response_id": "r1", "tool_call_count": 0}); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := NewLoggerWithID(dir, "session-two")
	if err != nil {
		t.Fatal(err)
	}
	_ = second.Append("session_metadata", map[string]any{"cwd": cwd})
	_ = second.Append("user_prompt", map[string]any{"text": "needle only in title"})
	_ = second.Append("model_response", map[string]any{"text": "other", "response_id": "r2", "tool_call_count": 0})
	_ = second.Close()
	if err := Rename(dir, "session-one", "  Renamed Needle  "); err != nil {
		t.Fatal(err)
	}
	info, err := InfoByID(dir, "session-one")
	if err != nil || info.Title != "Renamed Needle" || info.ModelID != "model-one" {
		t.Fatalf("renamed info=%#v err=%v", info, err)
	}
	result, err := Search(dir, SearchRequest{Query: "needle", CWD: cwd, Limit: 1, IncludeContent: true})
	if err != nil || len(result.Results) != 1 || result.NextOffset == nil || *result.NextOffset != 1 || result.TotalEstimate == nil || *result.TotalEstimate != 2 {
		t.Fatalf("search result=%#v err=%v", result, err)
	}
	if result.Results[0].SessionID != "session-one" || result.Results[0].Snippet == nil || *result.Results[0].Snippet != "needle result" {
		t.Fatalf("unexpected top search hit: %#v", result.Results[0])
	}
	artifact, err := ArtifactDir(first.Path())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(artifact, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(artifact, "data.txt"), []byte("artifact"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Delete(dir, "session-one"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(first.Path()); !os.IsNotExist(err) {
		t.Fatalf("session file survived delete: %v", err)
	}
	if _, err := os.Stat(artifact); !os.IsNotExist(err) {
		t.Fatalf("artifact directory survived delete: %v", err)
	}
}

func TestPromptHistoryOrderingAndScope(t *testing.T) {
	dir, cwd := t.TempDir(), t.TempDir()
	first, err := NewLoggerWithID(dir, "history-one")
	if err != nil {
		t.Fatal(err)
	}
	_ = first.Append("session_metadata", map[string]any{"cwd": cwd})
	_ = first.Append("user_prompt", map[string]any{"text": "first"})
	_ = first.Append("user_prompt", map[string]any{"text": "repeat"})
	_ = first.Close()
	time.Sleep(time.Millisecond)
	second, err := NewLoggerWithID(dir, "history-two")
	if err != nil {
		t.Fatal(err)
	}
	_ = second.Append("session_metadata", map[string]any{"cwd": cwd})
	_ = second.Append("user_prompt", map[string]any{"text": "repeat"})
	_ = second.Append("user_prompt", map[string]any{"text": "latest"})
	_ = second.Close()

	all, err := PromptHistory(dir, cwd, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(all, ","), "latest,repeat,first"; got != want {
		t.Fatalf("all history=%q want=%q", got, want)
	}
	chronological, err := PromptHistory(dir, cwd, "history-one", false)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(chronological, ","), "first,repeat"; got != want {
		t.Fatalf("session history=%q want=%q", got, want)
	}
	filtered, err := PromptHistory(dir, cwd, "history-one", true)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(filtered, ","), "repeat,first"; got != want {
		t.Fatalf("filtered history=%q want=%q", got, want)
	}
}

func TestSessionDeleteDoesNotFollowArtifactSymlink(t *testing.T) {
	dir := t.TempDir()
	logger, err := NewLoggerWithID(dir, "symlink-session")
	if err != nil {
		t.Fatal(err)
	}
	_ = logger.Append("session_metadata", map[string]any{"cwd": t.TempDir()})
	_ = logger.Close()
	artifact, _ := ArtifactDir(logger.Path())
	if err := os.MkdirAll(filepath.Dir(artifact), 0o700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	marker := filepath.Join(outside, "keep.txt")
	if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, artifact); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := Delete(dir, "symlink-session"); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "keep" {
		t.Fatalf("delete followed artifact symlink: %q err=%v", data, err)
	}
}
