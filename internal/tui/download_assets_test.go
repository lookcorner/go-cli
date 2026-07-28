package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lookcorner/go-cli/internal/agent"
)

func TestListSessionDownloads(t *testing.T) {
	dir := t.TempDir()
	sessionPath := filepath.Join(dir, "s.jsonl")
	downloads := filepath.Join(dir, "artifacts", "s", "downloads")
	if err := os.MkdirAll(downloads, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(downloads, "1.pdf"), []byte("%PDF"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := &model{runner: &agent.Runner{SessionPath: sessionPath}}
	list := m.listSessionDownloads()
	if !strings.Contains(list, "downloads/1.pdf") || !strings.Contains(list, "Session downloads (1)") {
		t.Fatalf("list=%q", list)
	}
	empty := &model{runner: &agent.Runner{SessionPath: filepath.Join(dir, "empty.jsonl")}}
	if got := empty.listSessionDownloads(); !strings.Contains(got, "No session downloads yet") {
		t.Fatalf("empty=%q", got)
	}
}
