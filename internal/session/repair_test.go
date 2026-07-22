package session

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
)

func TestRepairHistoryRepairsToolPairingAndIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repair.jsonl")
	writeRepairEvents(t, path, []Event{
		{Kind: "session_metadata", Data: map[string]any{"cwd": "/work"}},
		{Kind: "tool_call", Data: map[string]any{"step": 1, "call_id": "call-A", "name": "read_file"}},
		{Kind: "tool_result", Data: map[string]any{"call_id": "call-A", "output": "stale"}},
		{Kind: "tool_result", Data: map[string]any{"call_id": "call-A", "output": "real"}},
		{Kind: "tool_call", Data: map[string]any{"step": 1, "call_id": "call-B", "name": "grep"}},
		{Kind: "user_prompt", Data: map[string]any{"text": "boundary"}},
		{Kind: "tool_result", Data: map[string]any{"call_id": "call-B", "output": "late"}},
		{Kind: "tool_result", Data: map[string]any{"call_id": "call-lost", "output": "orphan"}},
	})
	report, err := RepairHistory(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if report.DuplicatesRemoved != 1 || report.SyntheticResultsInserted != 1 || !slices.Equal(report.StrippedToolResultIDs, []string{"call-B", "call-lost"}) {
		t.Fatalf("report=%#v", report)
	}
	results, err := Events(path, "tool_result")
	if err != nil || len(results) != 2 {
		t.Fatalf("results=%#v err=%v", results, err)
	}
	first := results[0].Data.(map[string]any)
	second := results[1].Data.(map[string]any)
	if first["call_id"] != "call-A" || first["output"] != "real" || second["call_id"] != "call-B" || second["synthetic"] != true || second["output"] != "Tool execution was halted by the harness (history_repair); the tool `grep` was not executed." {
		t.Fatalf("results=%#v", results)
	}
	secondReport, err := RepairHistory(path, false)
	if err != nil || secondReport.Changed() {
		t.Fatalf("second report=%#v err=%v", secondReport, err)
	}
}

func TestRepairHistoryDryRunDoesNotWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dry-run.jsonl")
	writeRepairEvents(t, path, []Event{{Kind: "tool_result", Data: map[string]any{"call_id": "orphan"}}})
	before, _ := os.ReadFile(path)
	report, err := RepairHistory(path, true)
	after, _ := os.ReadFile(path)
	if err != nil || !report.Changed() || !slices.Equal(before, after) {
		t.Fatalf("report=%#v err=%v before=%q after=%q", report, err, before, after)
	}
}

func TestRepairHistoryRecoversOrphanAfterMalformedOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "malformed.jsonl")
	content := "" +
		`{"kind":"user_prompt","data":{"text":"before"}}` + "\n" +
		`{"kind":"tool_call","data":` + "\n" +
		`{"kind":"tool_result","data":{"call_id":"lost","output":"orphan"}}` + "\n" +
		`{"kind":"model_response","data":{"response_id":"done","text":"after","tool_call_count":0}}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := RepairHistory(path, false)
	if err != nil || !slices.Equal(report.StrippedToolResultIDs, []string{"lost"}) {
		t.Fatalf("report=%#v err=%v", report, err)
	}
	data, err := os.ReadFile(path)
	if err != nil || bytes.Contains(data, []byte(`"call_id":"lost"`)) || bytes.Contains(data, []byte(`"data":\n`)) {
		t.Fatalf("repaired data=%q err=%v", data, err)
	}
	messages, err := Transcript(path)
	if err != nil || len(messages) != 2 || messages[1].Text != "after" {
		t.Fatalf("messages=%#v err=%v", messages, err)
	}
}

func TestLoggerRepairHistoryReopensForAppend(t *testing.T) {
	logger, err := NewLoggerWithID(t.TempDir(), "resident-repair")
	if err != nil {
		t.Fatal(err)
	}
	if err := logger.Append("tool_result", map[string]any{"call_id": "orphan"}); err != nil {
		t.Fatal(err)
	}
	report, err := logger.RepairHistory(false)
	if err != nil || !report.Changed() {
		t.Fatalf("report=%#v err=%v", report, err)
	}
	if err := logger.Append("user_prompt", map[string]any{"text": "after repair"}); err != nil {
		t.Fatal(err)
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	events, err := Events(logger.Path())
	if err != nil || len(events) != 1 || events[0].Kind != "user_prompt" {
		t.Fatalf("events=%#v err=%v", events, err)
	}
	info, err := os.Stat(logger.Path())
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%v err=%v", info.Mode(), err)
	}
}

func TestRepairHistoryRejectsSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privileges on Windows")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "target.jsonl")
	link := filepath.Join(dir, "link.jsonl")
	writeRepairEvents(t, target, []Event{{Kind: "tool_result", Data: map[string]any{"call_id": "orphan"}}})
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := RepairHistory(link, false); err == nil {
		t.Fatal("symlink session history was accepted")
	}
	results, err := Events(target, "tool_result")
	if err != nil || len(results) != 1 {
		t.Fatalf("target changed: results=%#v err=%v", results, err)
	}
}

func TestOpenStableAppendRejectsSymlinkAndUnexpectedFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privileges on Windows")
	}
	dir := t.TempDir()
	first := filepath.Join(dir, "first.jsonl")
	second := filepath.Join(dir, "second.jsonl")
	link := filepath.Join(dir, "link.jsonl")
	if err := os.WriteFile(first, []byte("first\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte("second\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(first, link); err != nil {
		t.Fatal(err)
	}
	if file, err := openStableAppend(link, nil); err == nil {
		_ = file.Close()
		t.Fatal("symlink reopen was accepted")
	}
	firstInfo, err := os.Stat(first)
	if err != nil {
		t.Fatal(err)
	}
	if file, err := openStableAppend(second, firstInfo); err == nil {
		_ = file.Close()
		t.Fatal("unexpected replacement file was accepted")
	}
}

func writeRepairEvents(t *testing.T, path string, events []Event) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	encoder := json.NewEncoder(file)
	for _, event := range events {
		if err := encoder.Encode(event); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}
