package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lookcorner/go-cli/internal/agent"
)

func TestListSessionWebFetch(t *testing.T) {
	dir := t.TempDir()
	sessionPath := filepath.Join(dir, "s.jsonl")
	root := filepath.Join(dir, "artifacts", "s", "web_fetch")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "1.md"), []byte("# doc"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := &model{runner: &agent.Runner{SessionPath: sessionPath}}
	list := m.listSessionWebFetch()
	if !strings.Contains(list, "web_fetch/1.md") || !strings.Contains(list, "web_fetch artifacts (1)") {
		t.Fatalf("list=%q", list)
	}
	if !strings.Contains(list, "/fetched") {
		t.Fatalf("missing show hint: %q", list)
	}
}

func TestShowSessionWebFetchByName(t *testing.T) {
	dir := t.TempDir()
	sessionPath := filepath.Join(dir, "s.jsonl")
	root := filepath.Join(dir, "artifacts", "s", "web_fetch")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	body := "# hello fetched\nline two"
	if err := os.WriteFile(filepath.Join(root, "2.md"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	m := &model{runner: &agent.Runner{SessionPath: sessionPath}}
	shown := m.sessionWebFetch("2.md")
	if !strings.Contains(shown, "`web_fetch/2.md`") || !strings.Contains(shown, "# hello fetched") {
		t.Fatalf("shown=%q", shown)
	}
	shown = m.sessionWebFetch("web_fetch/2.md")
	if !strings.Contains(shown, "line two") {
		t.Fatalf("uri shown=%q", shown)
	}
	if got := m.sessionWebFetch("missing.md"); !strings.Contains(got, "Couldn't open") {
		t.Fatalf("missing=%q", got)
	}
}

func TestShowSessionWebFetchTruncates(t *testing.T) {
	dir := t.TempDir()
	sessionPath := filepath.Join(dir, "s.jsonl")
	root := filepath.Join(dir, "artifacts", "s", "web_fetch")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	big := strings.Repeat("a", maxWebFetchShowBytes+64)
	if err := os.WriteFile(filepath.Join(root, "big.txt"), []byte(big), 0o600); err != nil {
		t.Fatal(err)
	}
	m := &model{runner: &agent.Runner{SessionPath: sessionPath}}
	shown := m.sessionWebFetch("big.txt")
	if !strings.Contains(shown, "[truncated]") || !strings.Contains(shown, "showing first") {
		t.Fatalf("shown=%q", shown)
	}
}
