package workspace

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadInstructions(t *testing.T) {
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
	files, err := ws.loadInstructions("", "")
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

func TestLoadInstructionsWithNoFiles(t *testing.T) {
	ws, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	files, err := ws.loadInstructions("", "")
	if err != nil || len(files) != 0 {
		t.Fatalf("expected empty instructions, files=%#v err=%v", files, err)
	}
}

func TestLoadInstructionsFromGitRootToWorkspace(t *testing.T) {
	repo := t.TempDir()
	command := exec.Command("git", "init", "-q")
	command.Dir = repo
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	cwd := filepath.Join(repo, "services", "api")
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		filepath.Join(repo, "AGENTS.md"):              "root",
		filepath.Join(repo, "services", "Claude.md"):  "service",
		filepath.Join(cwd, "AGENTS.md"):               "api",
		filepath.Join(repo, "services", "ignored.md"): "not an instruction name",
	}
	for path, content := range files {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ws, err := Open(cwd)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := ws.loadInstructions("", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 3 {
		t.Fatalf("unexpected scoped instructions: %#v", loaded)
	}
	want := []string{"AGENTS.md", "services/Claude.md", "services/api/AGENTS.md"}
	for index, path := range want {
		if !strings.EqualFold(loaded[index].Path, path) {
			t.Fatalf("instruction %d path = %q, want %q; all=%#v", index, loaded[index].Path, path, loaded)
		}
	}
}

func TestLoadInstructionsIncludesHomeBeforeProject(t *testing.T) {
	home := t.TempDir()
	grokHome := filepath.Join(home, "custom-grok-home")
	repo := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(filepath.Join(repo, "service"), 0o700); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("git", "init", "-q")
	command.Dir = repo
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	paths := []struct {
		path    string
		content string
	}{
		{filepath.Join(grokHome, "AGENTS.md"), "grok home"},
		{filepath.Join(home, ".claude", "CLAUDE.md"), "claude home"},
		{filepath.Join(home, ".cursor", "AGENTS.md"), "cursor home"},
		{filepath.Join(repo, "AGENTS.md"), "project root"},
		{filepath.Join(repo, "service", "AGENTS.md"), "project service"},
	}
	for _, file := range paths {
		if err := os.MkdirAll(filepath.Dir(file.path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file.path, []byte(file.content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("*.md\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ws, err := Open(filepath.Join(repo, "service"))
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := ws.loadInstructions(home, grokHome)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 3 {
		t.Fatalf("expected only three non-project home instructions, got %#v", loaded)
	}
	want := []string{"grok home", "claude home", "cursor home"}
	for index, content := range want {
		if loaded[index].Content != content {
			t.Fatalf("instruction %d content = %q, want %q; all=%#v", index, loaded[index].Content, content, loaded)
		}
		if !filepath.IsAbs(loaded[index].Path) {
			t.Fatalf("home instruction path should be absolute: %q", loaded[index].Path)
		}
	}
}
