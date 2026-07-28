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
}
