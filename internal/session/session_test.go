package session

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var testPNG = []byte{137, 80, 78, 71, 13, 10, 26, 10, 0, 0, 0, 13, 73, 72, 68, 82, 0, 0, 0, 1, 0, 0, 0, 1, 8, 2, 0, 0, 0, 144, 119, 83, 222, 0, 0, 0, 12, 73, 68, 65, 84, 8, 215, 99, 248, 207, 192, 0, 0, 3, 1, 1, 0, 24, 221, 141, 176, 0, 0, 0, 0, 73, 69, 78, 68, 174, 66, 96, 130}

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
		`{"kind":"user_prompt","data":{"text":"incomplete","content":[{"type":"image","uri":"../missing.png","mimeType":"image/png"}]}}` + "\n" +
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

func TestPromptImagesPersistOutsideJSONLAndReplay(t *testing.T) {
	dir := t.TempDir()
	logger, err := NewLoggerWithID(dir, "images")
	if err != nil {
		t.Fatal(err)
	}
	encoded := base64.StdEncoding.EncodeToString(testPNG)
	content := []Content{
		{Type: "text", Text: "inspect this"},
		{Type: "image", URI: "data:image/png;base64," + encoded},
		{Type: "image", URI: "https://example.com/remote.png"},
	}
	if err := logger.AppendPrompt("inspect this", content); err != nil {
		t.Fatal(err)
	}
	if err := logger.Append("model_response", map[string]any{"response_id": "r1", "text": "done", "tool_call_count": 0}); err != nil {
		t.Fatal(err)
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	logData, err := os.ReadFile(filepath.Join(dir, "images.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(logData), encoded) {
		t.Fatal("base64 image was written into the JSONL log")
	}
	assets, err := os.ReadDir(filepath.Join(dir, "assets"))
	if err != nil || len(assets) != 1 {
		t.Fatalf("unexpected assets: %#v err=%v", assets, err)
	}
	info, err := assets[0].Info()
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("unexpected asset permissions: %v", info.Mode().Perm())
	}
	messages, err := Transcript(filepath.Join(dir, "images.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || len(messages[0].Content) != 3 || messages[0].Content[1].Data != encoded || messages[0].Content[2].URI != "https://example.com/remote.png" {
		t.Fatalf("unexpected multimodal transcript: %#v", messages)
	}
	formatted := FormatTranscript(messages)
	if strings.Contains(formatted, encoded) || !strings.Contains(formatted, "[Image]") || !strings.Contains(formatted, "[Image: https://example.com/remote.png]") {
		t.Fatalf("unexpected formatted transcript: %q", formatted)
	}
}

func TestTranscriptRejectsEscapingImageAsset(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "unsafe.jsonl")
	content := "" +
		`{"kind":"user_prompt","data":{"text":"inspect","content":[{"type":"image","uri":"../secret.png","mimeType":"image/png"}]}}` + "\n" +
		`{"kind":"model_response","data":{"response_id":"r1","text":"done","tool_call_count":0}}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Transcript(path); err == nil || !strings.Contains(err.Error(), "invalid session image path") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestTranscriptRejectsSymlinkedAssetsDirectory(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "image.png"), testPNG, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "assets")); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "unsafe.jsonl")
	content := "" +
		`{"kind":"user_prompt","data":{"text":"inspect","content":[{"type":"image","uri":"assets/image.png","mimeType":"image/png"}]}}` + "\n" +
		`{"kind":"model_response","data":{"response_id":"r1","text":"done","tool_call_count":0}}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Transcript(path); err == nil || !strings.Contains(err.Error(), "non-symlink directory") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNamedSessionMetadataAndList(t *testing.T) {
	dir := t.TempDir()
	logger, err := NewLoggerWithID(dir, "acp-session-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := logger.Append("session_metadata", map[string]any{"cwd": "/workspace/project", "headCommit": "abc123"}); err != nil {
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
	if len(items) != 1 || items[0].SessionID != "acp-session-1" || items[0].Title != "Implement the persistent session support" || items[0].HeadCommit != "abc123" {
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

func TestForkCopiesTranscriptAndRebindsCWD(t *testing.T) {
	dir := t.TempDir()
	logger, err := NewLoggerWithID(dir, "parent")
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []struct {
		kind string
		data any
	}{
		{"session_metadata", map[string]any{"cwd": "/old"}},
		{"user_prompt", map[string]any{"text": "hello"}},
		{"model_response", map[string]any{"text": "world", "response_id": "r1", "tool_call_count": 0}},
	} {
		if err := logger.Append(event.kind, event.data); err != nil {
			t.Fatal(err)
		}
	}
	_ = logger.Close()
	chat, updates, err := Fork(dir, "parent", "child", "/new")
	if err != nil || chat != 2 || updates != 3 {
		t.Fatalf("fork counts: chat=%d updates=%d err=%v", chat, updates, err)
	}
	items, err := List(dir, "/new")
	if err != nil || len(items) != 1 || items[0].SessionID != "child" {
		t.Fatalf("forked session metadata: %#v err=%v", items, err)
	}
	messages, err := Transcript(filepath.Join(dir, "child.jsonl"))
	if err != nil || len(messages) != 2 || messages[0].Text != "hello" || messages[1].Text != "world" {
		t.Fatalf("forked transcript: %#v err=%v", messages, err)
	}
}
