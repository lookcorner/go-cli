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

func TestHunkTrackerAcceptAndRejectActions(t *testing.T) {
	root, registry := newHunkFixture(t, map[string]string{"tracked.txt": "before\n"})
	if _, err := registry.Execute(context.Background(), "edit_file", json.RawMessage(`{
		"path":"tracked.txt","old_text":"before","new_text":"after"
	}`)); err != nil {
		t.Fatal(err)
	}
	hunks, err := registry.HunkTracker().Hunks(context.Background(), "", "all")
	if err != nil || len(hunks) != 1 {
		t.Fatalf("unexpected hunks: %#v err=%v", hunks, err)
	}
	if count, err := registry.HunkTracker().HunkAction(context.Background(), hunks[0].ID, "accept"); err != nil || count != 1 {
		t.Fatalf("accept: count=%d err=%v", count, err)
	}
	visible, err := registry.HunkTracker().Hunks(context.Background(), "", "all")
	if err != nil || len(visible) != 0 {
		t.Fatalf("accepted hunk remained visible: %#v err=%v", visible, err)
	}
	data, err := os.ReadFile(filepath.Join(root, "tracked.txt"))
	if err != nil || string(data) != "after\n" {
		t.Fatalf("accept changed file: %q err=%v", data, err)
	}

	root2, registry2 := newHunkFixture(t, map[string]string{"tracked.txt": "before\n"})
	if _, err := registry2.Execute(context.Background(), "edit_file", json.RawMessage(`{
		"path":"tracked.txt","old_text":"before","new_text":"after"
	}`)); err != nil {
		t.Fatal(err)
	}
	hunks, err = registry2.HunkTracker().Hunks(context.Background(), "", "all")
	if err != nil || len(hunks) != 1 {
		t.Fatalf("unexpected reject hunks: %#v err=%v", hunks, err)
	}
	if count, err := registry2.HunkTracker().HunkAction(context.Background(), hunks[0].ID, "reject"); err != nil || count != 1 {
		t.Fatalf("reject: count=%d err=%v", count, err)
	}
	data, err = os.ReadFile(filepath.Join(root2, "tracked.txt"))
	if err != nil || string(data) != "before\n" {
		t.Fatalf("reject did not restore file: %q err=%v", data, err)
	}
}

func TestHunkTrackerAllRejectIsValidatedBeforeWriting(t *testing.T) {
	root, registry := newHunkFixture(t, map[string]string{"a.txt": "a0\n", "b.txt": "b0\n"})
	for _, edit := range []string{
		`{"path":"a.txt","old_text":"a0","new_text":"a1"}`,
		`{"path":"b.txt","old_text":"b0","new_text":"b1"}`,
	} {
		if _, err := registry.Execute(context.Background(), "edit_file", json.RawMessage(edit)); err != nil {
			t.Fatal(err)
		}
	}
	if count, err := registry.HunkTracker().AllAction(context.Background(), "reject"); err != nil || count != 2 {
		t.Fatalf("reject all: count=%d err=%v", count, err)
	}
	for name, want := range map[string]string{"a.txt": "a0\n", "b.txt": "b0\n"} {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil || string(data) != want {
			t.Fatalf("%s = %q, want %q (err=%v)", name, data, want, err)
		}
	}
}

func TestHunkTrackerRejectsCreatedAndDeletedFiles(t *testing.T) {
	root, registry := newHunkFixture(t, map[string]string{"deleted.txt": "restore me\n"})
	created := filepath.Join(root, "created.txt")
	if err := os.WriteFile(created, []byte("temporary\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if count, err := registry.HunkTracker().FileAction(context.Background(), "created.txt", "reject"); err != nil || count != 1 {
		t.Fatalf("reject created file: count=%d err=%v", count, err)
	}
	if _, err := os.Stat(created); !os.IsNotExist(err) {
		t.Fatalf("rejected created file still exists: %v", err)
	}

	deleted := filepath.Join(root, "deleted.txt")
	if err := os.Remove(deleted); err != nil {
		t.Fatal(err)
	}
	hunks, err := registry.HunkTracker().Hunks(context.Background(), "deleted.txt", "all")
	if err != nil || len(hunks) != 1 {
		t.Fatalf("deleted-file hunks: %#v err=%v", hunks, err)
	}
	if count, err := registry.HunkTracker().HunkAction(context.Background(), hunks[0].ID, "reject"); err != nil || count != 1 {
		t.Fatalf("reject deletion: count=%d err=%v", count, err)
	}
	data, err := os.ReadFile(deleted)
	if err != nil || string(data) != "restore me\n" {
		t.Fatalf("deleted file was not restored: %q err=%v", data, err)
	}
}

func TestRejectTextFailsClosedAndHandlesNoFinalNewline(t *testing.T) {
	hunk := Hunk{NewStart: 1, NewLines: 1, OldText: "old", NewText: "new"}
	if _, err := rejectText("later", hunk); err == nil {
		t.Fatal("stale hunk was accepted")
	}
	got, err := rejectText("new", hunk)
	if err != nil || got != "old" {
		t.Fatalf("no-newline reject = %q, err=%v", got, err)
	}
	if _, err := rejectText("new\n", hunk); err == nil {
		// The exact match intentionally distinguishes a trailing newline.
		t.Fatal("newline mismatch was accepted")
	}
}

func newHunkFixture(t *testing.T, files map[string]string) (string, *Registry) {
	t.Helper()
	root := t.TempDir()
	runGit(t, root, "init", "-q")
	for name, contents := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
		runGit(t, root, "add", name)
	}
	runGit(t, root, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "baseline")
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry(ws, PromptApprover{Mode: PermissionAuto})
	t.Cleanup(func() { _ = registry.Close() })
	return root, registry
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}
