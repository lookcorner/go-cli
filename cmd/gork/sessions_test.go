package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sessionlog "github.com/lookcorner/go-cli/internal/session"
	worktrees "github.com/lookcorner/go-cli/internal/worktree"
)

func TestSessionsCLIListSearchAndDelete(t *testing.T) {
	dir := t.TempDir()
	cwd := t.TempDir()
	writeCLISession(t, dir, "session-one", cwd, "Implement sessions", "local search needle")
	writeCLISession(t, dir, "session-two", cwd, "Second session", "other response")
	writeCLISession(t, dir, "outside", t.TempDir(), "Other workspace", "search needle")

	var stdout, stderr bytes.Buffer
	if err := runSessionsCommand(dir, cwd, []string{"list", "--limit", "1"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if text := stdout.String(); !strings.Contains(text, "SESSION ID") || strings.Count(text, "session-") != 1 || strings.Contains(text, "outside") {
		t.Fatalf("list output:\n%s", text)
	}

	stdout.Reset()
	if err := runSessionsCommand(dir, cwd, []string{"search", "needle", "-n", "5"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"session-one", "Implement sessions", "local search needle", "Total: 1"} {
		if !strings.Contains(stdout.String(), expected) {
			t.Fatalf("search output missing %q:\n%s", expected, stdout.String())
		}
	}
	if strings.Contains(stdout.String(), "outside") {
		t.Fatalf("search crossed workspace boundary:\n%s", stdout.String())
	}

	stdout.Reset()
	if err := runSessionsCommand(dir, cwd, []string{"delete", "session-one"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "Deleted session session-one\n" {
		t.Fatalf("delete output=%q", stdout.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "session-one.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("session was not deleted: %v", err)
	}
	stdout.Reset()
	if err := runSessionsCommand(dir, cwd, []string{"delete", "session-one"}, &stdout, &stderr); err != nil || !strings.Contains(stdout.String(), "No session found") {
		t.Fatalf("missing delete output=%q err=%v", stdout.String(), err)
	}
}

func TestSessionsCLIIncludesAndGroupsSiblingWorktrees(t *testing.T) {
	dir := t.TempDir()
	repo := t.TempDir()
	feature := t.TempDir()
	other := t.TempDir()
	writeCLISession(t, dir, "main-session", repo, "Main work", "main response")
	writeCLISession(t, dir, "feature-session", feature, "Feature work", "sibling needle")
	writeCLISession(t, dir, "outside-session", other, "Outside work", "sibling needle")
	records := []worktrees.Record{{
		ID: "wt-feature", Path: feature, SourceRepo: repo, Kind: "session",
		Status: "alive", Label: "Feature One", SessionID: "feature-session",
	}}
	data, err := json.Marshal(records)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "worktrees.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if err := runSessionsCommand(dir, repo, []string{"list"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	text := stdout.String()
	for _, expected := range []string{"Label: Feature One", "feature-session", "(no label)", "main-session"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("list output missing %q:\n%s", expected, text)
		}
	}
	if strings.Contains(text, "outside-session") {
		t.Fatalf("list crossed repository boundary:\n%s", text)
	}

	stdout.Reset()
	if err := runSessionsCommand(dir, repo, []string{"search", "sibling", "--limit", "5"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if text := stdout.String(); !strings.Contains(text, "feature-session") || strings.Contains(text, "outside-session") || !strings.Contains(text, "Total: 1") {
		t.Fatalf("search output:\n%s", text)
	}
}

func TestSessionsCLIArgumentsAndEmptyStates(t *testing.T) {
	dir, cwd := t.TempDir(), t.TempDir()
	var stdout, stderr bytes.Buffer
	if err := runSessionsCommand(dir, cwd, []string{"list", "-n", "0"}, &stdout, &stderr); err != nil || stdout.String() != "No sessions found.\n" {
		t.Fatalf("empty list=%q err=%v", stdout.String(), err)
	}
	stdout.Reset()
	if err := runSessionsCommand(dir, cwd, []string{"search", "--limit=0", "anything"}, &stdout, &stderr); err != nil || !strings.Contains(stdout.String(), "Total: 0") {
		t.Fatalf("zero search=%q err=%v", stdout.String(), err)
	}
	stdout.Reset()
	if err := runSessionsCommand(dir, cwd, []string{"search", "--", "-missing"}, &stdout, &stderr); err != nil || !strings.Contains(stdout.String(), "Total: 0") {
		t.Fatalf("hyphen search=%q err=%v", stdout.String(), err)
	}
	for _, args := range [][]string{
		nil,
		{"unknown"},
		{"list", "extra"},
		{"list", "-n"},
		{"list", "-n", "-1"},
		{"list", "--limit="},
		{"list", "--bad"},
		{"search"},
		{"search", "one", "two"},
		{"delete"},
		{"delete", "../escape"},
	} {
		stdout.Reset()
		stderr.Reset()
		if err := runSessionsCommand(dir, cwd, args, &stdout, &stderr); err == nil {
			t.Fatalf("args=%v unexpectedly succeeded", args)
		}
	}
	if got := sessionLine("one\n two\u202e"); got != "one two" {
		t.Fatalf("session line=%q", got)
	}
}

func writeCLISession(t *testing.T, dir, id, cwd, prompt, response string) {
	t.Helper()
	logger, err := sessionlog.NewLoggerWithID(dir, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := logger.Append("session_metadata", map[string]any{"cwd": cwd}); err != nil {
		t.Fatal(err)
	}
	if err := logger.Append("user_prompt", map[string]any{"text": prompt}); err != nil {
		t.Fatal(err)
	}
	if err := logger.Append("model_response", map[string]any{"text": response, "response_id": "response", "tool_call_count": 0}); err != nil {
		t.Fatal(err)
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
}
