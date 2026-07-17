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

func TestTranscriptStopsAtLastCompletedTurn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	content := "" +
		`{"kind":"user_prompt","data":{"text":"first"}}` + "\n" +
		`{"kind":"model_response","data":{"response_id":"r1","text":"checking ","tool_call_count":1}}` + "\n" +
		`{"kind":"model_response","data":{"response_id":"r2","text":"done","tool_call_count":0}}` + "\n" +
		`{"kind":"user_prompt","data":{"text":"incomplete"}}` + "\n" +
		`{"kind":"model_response","data":{"response_id":"r3","text":"partial","tool_call_count":1}}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	messages, err := Transcript(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || messages[0].Text != "first" || messages[1].Text != "checking done" {
		t.Fatalf("unexpected transcript: %#v", messages)
	}
	formatted := FormatTranscript(messages)
	if formatted != "You\nfirst\n\nGork\nchecking done" {
		t.Fatalf("unexpected formatted transcript: %q", formatted)
	}
}

func TestNamedSessionMetadataAndList(t *testing.T) {
	dir := t.TempDir()
	logger, err := NewLoggerWithID(dir, "acp-session-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := logger.Append("session_metadata", map[string]any{"cwd": "/workspace/project"}); err != nil {
		t.Fatal(err)
	}
	if err := logger.Append("user_prompt", map[string]any{"text": "Implement the persistent session support\nwith tests"}); err != nil {
		t.Fatal(err)
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	items, err := List(dir, "/workspace/project")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].SessionID != "acp-session-1" || items[0].Title != "Implement the persistent session support" {
		t.Fatalf("unexpected session list: %#v", items)
	}
	path, err := PathForID(dir, "acp-session-1")
	if err != nil || filepath.Base(path) != "acp-session-1.jsonl" {
		t.Fatalf("unexpected session path: %q err=%v", path, err)
	}
	if _, err := PathForID(dir, "../escape"); err == nil {
		t.Fatal("unsafe session ID was accepted")
	}
}
