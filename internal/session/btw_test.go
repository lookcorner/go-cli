package session

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAppendBtwWritesReferenceJSONLAndRejectsWrongParent(t *testing.T) {
	logger, err := NewLoggerWithID(t.TempDir(), "session-1")
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	entry := BtwEntry{
		BtwSessionID: "btw-1", ParentSessionID: "session-1", AskedAt: time.Date(2026, 7, 22, 1, 2, 3, 0, time.UTC),
		Question: "What changed?", Answer: "The parser changed.", Model: "grok", Success: true,
	}
	if err := AppendBtw(logger.Path(), entry); err != nil {
		t.Fatal(err)
	}
	entry.BtwSessionID, entry.Success, entry.Answer, entry.Error = "btw-2", false, "", "offline"
	if err := AppendBtw(logger.Path(), entry); err != nil {
		t.Fatal(err)
	}
	path, _ := BtwHistoryPath(logger.Path())
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	var rows []map[string]any
	for scanner.Scan() {
		var row map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			t.Fatal(err)
		}
		rows = append(rows, row)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0]["btwSessionId"] != "btw-1" || rows[0]["parentSessionId"] != "session-1" || rows[0]["success"] != true || rows[1]["error"] != "offline" {
		t.Fatalf("rows=%#v", rows)
	}
	entry.ParentSessionID = "other"
	if err := AppendBtw(logger.Path(), entry); err == nil {
		t.Fatal("mismatched parent was accepted")
	}
}

func TestAppendBtwRejectsSymlinkedArtifactDirectory(t *testing.T) {
	dir := t.TempDir()
	logger, err := NewLoggerWithID(dir, "session-1")
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "artifacts")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	entry := BtwEntry{BtwSessionID: "btw-1", ParentSessionID: "session-1", AskedAt: time.Now(), Question: "question", Answer: "answer", Success: true}
	if err := AppendBtw(logger.Path(), entry); err == nil {
		t.Fatal("symlinked artifact directory was accepted")
	}
	if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
		t.Fatalf("outside entries=%v err=%v", entries, err)
	}
}
