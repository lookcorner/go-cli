package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	worktrees "github.com/lookcorner/go-cli/internal/worktree"
)

func TestWorktreeCLIListShowAndDB(t *testing.T) {
	state := t.TempDir()
	firstPath := filepath.Join(t.TempDir(), "first")
	secondPath := filepath.Join(t.TempDir(), "second")
	for _, path := range []string{firstPath, secondPath} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC().Add(-2 * time.Hour)
	records := []worktrees.Record{
		{ID: "wt-first", Path: firstPath, SourceRepo: "/repo/one", RepoName: "one", Kind: "session", CreationMode: "linked", GitRef: "feature", HeadCommit: strings.Repeat("a", 40), SessionID: "session-1", CreatedAt: now, LastAccessedAt: now, Status: "alive", Label: "First"},
		{ID: "wt-second", Path: secondPath, SourceRepo: "/repo/two", RepoName: "two", Kind: "cloud", CreationMode: "standalone", HeadCommit: strings.Repeat("b", 40), CreatedAt: now, LastAccessedAt: now, Status: "alive", Label: "Second"},
	}
	manager := testWorktreeManager(t, state, records)
	var stdout, stderr bytes.Buffer
	if err := runWorktreeCommand(context.Background(), manager, []string{"list", "--repo", "one"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if text := stdout.String(); !strings.Contains(text, "wt-first") || strings.Contains(text, "wt-second") || !strings.Contains(text, "feature") {
		t.Fatalf("table output:\n%s", text)
	}

	stdout.Reset()
	if err := runWorktreeCommand(context.Background(), manager, []string{"ls", "--type", "session,cloud", "--json"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	var listed []worktrees.Record
	if err := json.Unmarshal(stdout.Bytes(), &listed); err != nil || len(listed) != 2 {
		t.Fatalf("JSON list=%#v err=%v output=%s", listed, err, stdout.String())
	}

	stdout.Reset()
	if err := runWorktreeCommand(context.Background(), manager, []string{"show", "wt-first"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"Path:", firstPath, "ID:             wt-first", "HEAD:           aaaaaaaaaaaa", "Session ID:     session-1", "Disk Usage:"} {
		if !strings.Contains(stdout.String(), expected) {
			t.Fatalf("show output missing %q:\n%s", expected, stdout.String())
		}
	}

	stdout.Reset()
	if err := runWorktreeCommand(context.Background(), manager, []string{"db", "stats"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "Total records: 2") || !strings.Contains(stdout.String(), "Alive:         2") {
		t.Fatalf("stats output:\n%s", stdout.String())
	}
	stdout.Reset()
	if err := runWorktreeCommand(context.Background(), manager, []string{"db", "path"}, &stdout, &stderr); err != nil || strings.TrimSpace(stdout.String()) != filepath.Join(state, "worktrees.json") {
		t.Fatalf("path output=%q err=%v", stdout.String(), err)
	}
}

func TestWorktreeCLIRemoveDryRunAndGC(t *testing.T) {
	state := t.TempDir()
	path := filepath.Join(t.TempDir(), "kept")
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Add(-8 * 24 * time.Hour)
	manager := testWorktreeManager(t, state, []worktrees.Record{
		{ID: "wt-kept", Path: path, SourceRepo: "/repo", RepoName: "repo", Kind: "session", CreationMode: "linked", CreatedAt: now, LastAccessedAt: now, Status: "alive"},
		{ID: "wt-dead", Path: filepath.Join(t.TempDir(), "missing"), SourceRepo: "/repo", RepoName: "repo", Kind: "session", CreationMode: "linked", CreatedAt: now, LastAccessedAt: now, Status: "dead"},
	})
	var stdout, stderr bytes.Buffer
	if err := runWorktreeCommand(context.Background(), manager, []string{"rm", "wt-kept", "--dry-run"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "would remove: "+path) {
		t.Fatalf("dry-run output:\n%s", stdout.String())
	}
	if _, ok := manager.Show("wt-kept"); !ok {
		t.Fatal("dry run removed the record")
	}

	stdout.Reset()
	if err := runWorktreeCommand(context.Background(), manager, []string{"prune", "--dry-run", "--max-age=7d", "--force"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "Dry run") || !strings.Contains(stdout.String(), "Dead records removed:      1") || !strings.Contains(stdout.String(), "Expired worktrees removed: 1") {
		t.Fatalf("GC output:\n%s", stdout.String())
	}
	if _, ok := manager.Show("wt-dead"); !ok {
		t.Fatal("GC dry run removed the dead record")
	}
}

func TestWorktreeCLIRejectsInvalidArgumentsAndSanitizesOutput(t *testing.T) {
	manager := testWorktreeManager(t, t.TempDir(), nil)
	for _, args := range [][]string{
		nil,
		{"unknown"},
		{"show"},
		{"rm"},
		{"rm", "--bad"},
		{"gc", "--max-age", "soon"},
		{"gc", "--max-age="},
		{"gc", "--bad"},
		{"db"},
		{"db", "unknown"},
	} {
		var stdout, stderr bytes.Buffer
		if err := runWorktreeCommand(context.Background(), manager, args, &stdout, &stderr); err == nil {
			t.Fatalf("args=%v unexpectedly succeeded", args)
		}
	}
	if got := cleanWorktreeText("safe\x1b[31m\nnext"); got != "safe[31mnext" {
		t.Fatalf("sanitized=%q", got)
	}
	if duration, err := parseWorktreeAge("1.5d"); err != nil || duration != 36*time.Hour {
		t.Fatalf("duration=%v err=%v", duration, err)
	}
}

func testWorktreeManager(t *testing.T, state string, records []worktrees.Record) *worktrees.Manager {
	t.Helper()
	if records != nil {
		data, err := json.Marshal(records)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(state, "worktrees.json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	manager, err := worktrees.NewManager(state)
	if err != nil {
		t.Fatal(err)
	}
	return manager
}
