package acp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRestoreSessionCodeChecksOutPersistedHead(t *testing.T) {
	root := t.TempDir()
	runACPGit(t, root, "init", "-q")
	runACPGit(t, root, "config", "user.name", "Fixture")
	runACPGit(t, root, "config", "user.email", "fixture@example.invalid")
	file := filepath.Join(root, "tracked.txt")
	if err := os.WriteFile(file, []byte("first\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runACPGit(t, root, "add", "tracked.txt")
	runACPGit(t, root, "commit", "-qm", "first")
	first := strings.TrimSpace(runACPGitOutput(t, root, "rev-parse", "HEAD"))
	if err := os.WriteFile(file, []byte("second\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runACPGit(t, root, "add", "tracked.txt")
	runACPGit(t, root, "commit", "-qm", "second")

	meta := restoreSessionCode(context.Background(), root, first)
	if meta["restored"] != true || meta["degree"] != "head_only" || !strings.Contains(meta["summary"].(string), first[:7]) {
		t.Fatalf("meta=%#v", meta)
	}
	if got := string(mustReadFile(t, file)); got != "first\n" {
		t.Fatalf("restored content=%q", got)
	}
}

func TestRestoreSessionCodeReportsUnsafeCWD(t *testing.T) {
	meta := restoreSessionCode(context.Background(), t.TempDir(), "deadbeef")
	if meta["restored"] != false || meta["degree"] != nil {
		t.Fatalf("meta=%#v", meta)
	}
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
