package tools

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/lookcorner/go-cli/internal/workspace"
)

func TestHunkTrackerAttributesAgentAndExternalFiles(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init", "-q")
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "tracked.txt")
	runGit(t, root, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "baseline")
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry(ws, PromptApprover{Mode: PermissionAuto})
	defer registry.Close()
	if _, err := registry.Execute(context.Background(), "edit_file", json.RawMessage(`{
		"path":"tracked.txt","old_text":"before","new_text":"after"
	}`)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "external.txt"), []byte("outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	hunks, err := registry.HunkTracker().Hunks(context.Background(), "", "all")
	if err != nil {
		t.Fatal(err)
	}
	if len(hunks) != 2 {
		t.Fatalf("unexpected hunks: %#v", hunks)
	}
	sources := map[string]string{}
	for _, hunk := range hunks {
		sources[hunk.Path] = hunk.Source
		if hunk.ID == "" || hunk.Patch == "" {
			t.Fatalf("hunk missing identity or patch: %#v", hunk)
		}
	}
	if sources["tracked.txt"] != "agent" || sources["external.txt"] != "external" {
		t.Fatalf("unexpected attribution: %#v", sources)
	}
	runGit(t, root, "add", "tracked.txt")
	files, err := registry.HunkTracker().Files(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var staged bool
	for _, file := range files {
		if file.Path == "tracked.txt" {
			staged = file.Staged
		}
	}
	if !staged {
		t.Fatalf("staged file was not reported: %#v", files)
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}
