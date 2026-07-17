package session

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResumeUsesLastCompletedResponse(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	content := "" +
		`{"time":"2026-01-01T00:00:00Z","kind":"model_response","data":{"response_id":"resp_complete","tool_call_count":0}}` + "\n" +
		`{"time":"2026-01-01T00:00:01Z","kind":"model_response","data":{"response_id":"resp_pending","tool_call_count":1}}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	logger, responseID, err := Resume(path)
	if err != nil {
		t.Fatal(err)
	}
	if responseID != "resp_complete" {
		t.Fatalf("unexpected response ID: %q", responseID)
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) <= len(content) {
		t.Fatal("resume event was not appended")
	}
}

func TestResumeRejectsSessionWithoutCompletedTurn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	content := `{"kind":"model_response","data":{"response_id":"resp_pending","tool_call_count":2}}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Resume(path); err == nil {
		t.Fatal("expected incomplete session to be rejected")
	}
}

func TestLatest(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"20260101.jsonl", "20260201.jsonl", "ignore.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(""), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	path, err := Latest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "20260201.jsonl" {
		t.Fatalf("unexpected latest session: %s", path)
	}
}
