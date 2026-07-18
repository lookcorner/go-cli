package tools

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

func TestHunkTrackerAttributesMixedFilePerHunk(t *testing.T) {
	root, registry := newHunkFixture(t, map[string]string{"mixed.txt": "one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\n"})
	path := filepath.Join(root, "mixed.txt")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(string(data), "one", "user-one", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Execute(context.Background(), "edit_file", json.RawMessage(`{
		"path":"mixed.txt","old_text":"five","new_text":"agent-five"
	}`)); err != nil {
		t.Fatal(err)
	}
	hunks, err := registry.HunkTracker().Hunks(context.Background(), "mixed.txt", "all")
	if err != nil || len(hunks) != 2 {
		t.Fatalf("mixed hunks=%#v err=%v", hunks, err)
	}
	sources := map[string]string{}
	for _, hunk := range hunks {
		sources[strings.TrimSpace(hunk.NewText)] = hunk.Source
	}
	if sources["user-one"] != "external" || sources["agent-five"] != "agent" {
		t.Fatalf("mixed attribution=%#v", sources)
	}

	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(string(data), "nine", "user-nine", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	hunks, err = registry.HunkTracker().Hunks(context.Background(), "mixed.txt", "all")
	if err != nil || len(hunks) != 3 {
		t.Fatalf("later mixed hunks=%#v err=%v", hunks, err)
	}
	for _, hunk := range hunks {
		if strings.Contains(hunk.NewText, "agent-five") && hunk.Source != "agent" {
			t.Fatalf("agent hunk lost attribution after external edit: %#v", hunk)
		}
		if strings.Contains(hunk.NewText, "user-") && hunk.Source != "external" {
			t.Fatalf("external hunk was attributed to agent: %#v", hunk)
		}
	}
	runGit(t, root, "add", "mixed.txt")
	staged, err := registry.HunkTracker().Hunks(context.Background(), "mixed.txt", "all")
	if err != nil || len(staged) != 3 {
		t.Fatalf("staged mixed hunks=%#v err=%v", staged, err)
	}
	for _, hunk := range staged {
		if strings.Contains(hunk.NewText, "agent-five") && hunk.Source != "agent" {
			t.Fatalf("staging lost agent attribution: %#v", hunk)
		}
	}
}

func TestHunkTrackerAttributesAbsoluteToolPath(t *testing.T) {
	root, registry := newHunkFixture(t, map[string]string{"absolute.txt": "before\n"})
	arguments, err := json.Marshal(map[string]string{
		"path": filepath.Join(root, "absolute.txt"), "old_text": "before", "new_text": "after",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Execute(context.Background(), "edit_file", arguments); err != nil {
		t.Fatal(err)
	}
	hunks, err := registry.HunkTracker().Hunks(context.Background(), "absolute.txt", "all")
	if err != nil || len(hunks) != 1 || hunks[0].Source != "agent" {
		t.Fatalf("absolute path attribution=%#v err=%v", hunks, err)
	}
}

func TestHunkTrackerSnapshotFailureDoesNotClaimExistingHunks(t *testing.T) {
	root, registry := newHunkFixture(t, map[string]string{"external.txt": "before\n"})
	if err := os.WriteFile(filepath.Join(root, "external.txt"), []byte("after\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	registry.HunkTracker().markAgentChanges(context.Background(), "external.txt", nil)
	hunks, err := registry.HunkTracker().Hunks(context.Background(), "external.txt", "all")
	if err != nil || len(hunks) != 1 || hunks[0].Source != "external" {
		t.Fatalf("failed snapshot claimed external hunk: %#v err=%v", hunks, err)
	}
}

func TestHunkTrackerTurnActionUsesPromptIndex(t *testing.T) {
	root, registry := newHunkFixture(t, map[string]string{"turns.txt": "one\ntwo\nthree\nfour\nfive\n"})
	promptIndex := 0
	registry.SetRewindStore(nil, func() int { return promptIndex })
	if _, err := registry.Execute(context.Background(), "edit_file", json.RawMessage(`{
		"path":"turns.txt","old_text":"one","new_text":"turn-zero"
	}`)); err != nil {
		t.Fatal(err)
	}
	promptIndex = 1
	if _, err := registry.Execute(context.Background(), "edit_file", json.RawMessage(`{
		"path":"turns.txt","old_text":"five","new_text":"turn-one"
	}`)); err != nil {
		t.Fatal(err)
	}
	hunks, err := registry.HunkTracker().Hunks(context.Background(), "turns.txt", "agent")
	if err != nil || len(hunks) != 2 || hunks[0].PromptIndex == nil || hunks[1].PromptIndex == nil {
		t.Fatalf("turn hunks=%#v err=%v", hunks, err)
	}
	if count, err := registry.HunkTracker().TurnAction(context.Background(), 0, "reject"); err != nil || count != 1 {
		t.Fatalf("reject turn zero: count=%d err=%v", count, err)
	}
	data, err := os.ReadFile(filepath.Join(root, "turns.txt"))
	if err != nil || string(data) != "one\ntwo\nthree\nfour\nturn-one\n" {
		t.Fatalf("turn reject content=%q err=%v", data, err)
	}
	if count, err := registry.HunkTracker().TurnAction(context.Background(), 1, "accept"); err != nil || count != 1 {
		t.Fatalf("accept turn one: count=%d err=%v", count, err)
	}
	if _, err := registry.HunkTracker().TurnAction(context.Background(), -1, "accept"); err == nil {
		t.Fatal("negative prompt index was accepted")
	}
}

func TestHunkTrackerSessionSummaryTracksTurnsAndActions(t *testing.T) {
	root, registry := newHunkFixture(t, map[string]string{"turns.txt": "one\ntwo\nthree\n"})
	promptIndex := 2
	registry.SetRewindStore(nil, func() int { return promptIndex })
	if _, err := registry.Execute(context.Background(), "edit_file", json.RawMessage(`{
		"path":"turns.txt","old_text":"one","new_text":"agent-one"
	}`)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "external.txt"), []byte("outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	summary, err := registry.HunkTracker().Summary(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Turns) != 1 || summary.Turns[0].PromptIndex != 2 || summary.PendingHunks != 1 || summary.UnattributedPending != 1 || summary.FilesModified != 1 {
		t.Fatalf("unexpected pending summary: %#v", summary)
	}
	if count, err := registry.HunkTracker().TurnAction(context.Background(), 2, "accept"); err != nil || count != 1 {
		t.Fatalf("accept turn: count=%d err=%v", count, err)
	}
	summary, err = registry.HunkTracker().Summary(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if summary.Stats.AcceptedHunks != 1 || summary.Stats.AcceptedLinesAdded != 1 || summary.PendingHunks != 0 || len(summary.Turns) != 0 || summary.UnattributedPending != 1 {
		t.Fatalf("unexpected accepted summary: %#v", summary)
	}
}

func TestHunkTrackerStateSurvivesRegistryRestart(t *testing.T) {
	root, first := newHunkFixture(t, map[string]string{"accepted.txt": "before\n", "pending.txt": "before\n"})
	artifactDir := t.TempDir()
	if err := first.ConfigureHunkState(artifactDir); err != nil {
		t.Fatal(err)
	}
	promptIndex := 4
	first.SetRewindStore(nil, func() int { return promptIndex })
	for _, name := range []string{"accepted.txt", "pending.txt"} {
		arguments, _ := json.Marshal(map[string]string{"path": name, "old_text": "before", "new_text": "after"})
		if _, err := first.Execute(context.Background(), "edit_file", arguments); err != nil {
			t.Fatal(err)
		}
	}
	hunks, err := first.HunkTracker().Hunks(context.Background(), "accepted.txt", "agent")
	if err != nil || len(hunks) != 1 {
		t.Fatalf("accepted fixture hunks=%#v err=%v", hunks, err)
	}
	if _, err := first.HunkTracker().HunkAction(context.Background(), hunks[0].ID, "accept"); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	second := NewRegistry(ws, PromptApprover{Mode: PermissionAuto})
	t.Cleanup(func() { _ = second.Close() })
	if err := second.ConfigureHunkState(artifactDir); err != nil {
		t.Fatal(err)
	}
	restored, err := second.HunkTracker().Hunks(context.Background(), "", "agent")
	if err != nil || len(restored) != 1 || restored[0].Path != "pending.txt" || restored[0].PromptIndex == nil || *restored[0].PromptIndex != 4 {
		t.Fatalf("restored hunks=%#v err=%v", restored, err)
	}
	summary, err := second.HunkTracker().Summary(context.Background())
	if err != nil || summary.Stats.AcceptedHunks != 1 || summary.PendingHunks != 1 {
		t.Fatalf("restored summary=%#v err=%v", summary, err)
	}
	contents, err := second.HunkTracker().AllFileContents(context.Background())
	if err != nil || len(contents) != 2 || !contents[0].IsAgentFile || !contents[1].IsAgentFile {
		t.Fatalf("restored agent files=%#v err=%v", contents, err)
	}
}

func TestHunkTrackerStateRejectsCorruptFile(t *testing.T) {
	_, registry := newHunkFixture(t, map[string]string{"tracked.txt": "before\n"})
	artifactDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(artifactDir, "hunks.json"), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := registry.ConfigureHunkState(artifactDir); err == nil {
		t.Fatal("corrupt hunk state was accepted")
	}
	if err := registry.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(artifactDir, "hunks.json"))
	if err != nil || string(data) != "not json" {
		t.Fatalf("corrupt state was overwritten: %q err=%v", data, err)
	}
}

func TestHunkTrackerHeadChangeDropsStaleHunkIdentity(t *testing.T) {
	root, registry := newHunkFixture(t, map[string]string{"tracked.txt": "before\n"})
	originalHead := runGitOutput(t, root, "rev-parse", "HEAD")
	promptIndex := 0
	registry.SetRewindStore(nil, func() int { return promptIndex })
	if _, err := registry.Execute(context.Background(), "edit_file", json.RawMessage(`{
		"path":"tracked.txt","old_text":"before","new_text":"after"
	}`)); err != nil {
		t.Fatal(err)
	}
	hunks, err := registry.HunkTracker().Hunks(context.Background(), "tracked.txt", "agent")
	if err != nil || len(hunks) != 1 {
		t.Fatalf("agent hunks=%#v err=%v", hunks, err)
	}
	if _, err := registry.HunkTracker().HunkAction(context.Background(), hunks[0].ID, "accept"); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "tracked.txt")
	runGit(t, root, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "accepted")
	runGit(t, root, "checkout", "-q", "--detach", originalHead)
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("after\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	hunks, err = registry.HunkTracker().Hunks(context.Background(), "tracked.txt", "all")
	if err != nil || len(hunks) != 1 || hunks[0].Source != "external" {
		t.Fatalf("stale identity survived HEAD change: %#v err=%v", hunks, err)
	}
	files, err := registry.HunkTracker().Files(context.Background())
	if err != nil || len(files) != 1 || !files[0].IsAgentFile {
		t.Fatalf("agent file identity was lost: %#v err=%v", files, err)
	}
	summary, err := registry.HunkTracker().Summary(context.Background())
	if err != nil || summary.Stats.AcceptedHunks != 1 || summary.UnattributedPending != 1 {
		t.Fatalf("HEAD-change summary=%#v err=%v", summary, err)
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

func runGitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}
