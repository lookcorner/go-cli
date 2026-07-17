package workspace

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadRootInstructions(t *testing.T) {
	root := t.TempDir()
	command := exec.Command("git", "init", "-q")
	command.Dir = root
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("root rule"), 0o600); err != nil {
		t.Fatal(err)
	}
	rulesDir := filepath.Join(root, ".gork", "rules")
	if err := os.MkdirAll(rulesDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rulesDir, "go.md"), []byte("---\npaths: '*.go'\n---\ngo rule"), 0o600); err != nil {
		t.Fatal(err)
	}
	ignoredDir := filepath.Join(root, ".cursor", "rules")
	if err := os.MkdirAll(ignoredDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ignoredDir, "ignored.md"), []byte("must not load"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte(".cursor/rules/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ws, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	files, err := ws.LoadRootInstructions()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("expected two instruction files, got %#v", files)
	}
	formatted := FormatInstructions(files)
	if !strings.Contains(formatted, "root rule") || !strings.Contains(formatted, "go rule") {
		t.Fatalf("missing instruction content: %s", formatted)
	}
	if strings.Contains(formatted, "paths: '*.go'") {
		t.Fatalf("rules frontmatter should be removed: %s", formatted)
	}
	if strings.Contains(formatted, "must not load") {
		t.Fatalf("gitignored rule was loaded: %s", formatted)
	}
}

func TestLoadRootInstructionsWithNoFiles(t *testing.T) {
	ws, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	files, err := ws.LoadRootInstructions()
	if err != nil || len(files) != 0 {
		t.Fatalf("expected empty instructions, files=%#v err=%v", files, err)
	}
}
