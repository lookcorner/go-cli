package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGoalScratchCreatesRestoresAndCleansPrivateDirectory(t *testing.T) {
	root, artifactDir := t.TempDir(), filepath.Join(t.TempDir(), "artifacts")
	registry := newPersistentGoalRegistry(t, root, artifactDir)
	if err := registry.BeginGoal("goal"); err != nil {
		t.Fatal(err)
	}
	scratch, ready := registry.GoalScratch()
	if !ready || scratch == "" || !pathWithin(artifactDir, scratch) {
		t.Fatalf("scratch=%q ready=%v", scratch, ready)
	}
	for _, path := range []string{filepath.Dir(scratch), scratch} {
		if info, err := os.Lstat(path); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
			t.Fatalf("private directory %q info=%#v err=%v", path, info, err)
		}
	}
	proof := filepath.Join(scratch, "proof.log")
	if err := os.WriteFile(proof, []byte("verified output\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	reader := registry.View(nil, nil, "read-only")
	output, err := reader.Execute(context.Background(), "read_file", json.RawMessage(`{"target_file":"`+proof+`"}`))
	if err != nil || !strings.Contains(output, "verified output") {
		t.Fatalf("read-only verifier output=%q err=%v", output, err)
	}
	if err := registry.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Dir(scratch)); err != nil {
		t.Fatal(err)
	}
	restored := newPersistentGoalRegistry(t, root, artifactDir)
	defer restored.Close()
	if got, ready := restored.GoalScratch(); got != scratch || !ready {
		t.Fatalf("restored scratch=%q ready=%v, want %q", got, ready, scratch)
	}
	if _, err := restored.ResumeGoal(); err != nil {
		t.Fatal(err)
	}
	if _, err := (&updateGoalTool{store: restored.goal}).Execute(context.Background(), json.RawMessage(`{"completed":true}`)); err != nil {
		t.Fatal(err)
	}
	if err := restored.StartGoalVerification(10); err != nil {
		t.Fatal(err)
	}
	if err := restored.ResolveGoalVerification(GoalVerification{Achieved: true, Summary: "verified"}, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Dir(scratch)); !os.IsNotExist(err) {
		t.Fatalf("terminal goal retained scratch root: %v", err)
	}
}

func TestGoalScratchRejectsSymlinkSquat(t *testing.T) {
	artifactDir, outside := t.TempDir(), t.TempDir()
	root, implementer := goalScratchPaths(artifactDir, "123e4567-e89b-42d3-a456-426614174000")
	if err := os.Symlink(outside, root); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	store := NewGoalStore()
	store.artifactDir, store.goalID = artifactDir, "123e4567-e89b-42d3-a456-426614174000"
	store.prepareScratchLocked()
	if store.scratchReady || store.scratchDir != implementer {
		t.Fatalf("scratch=%q ready=%v", store.scratchDir, store.scratchReady)
	}
	if _, err := os.Lstat(filepath.Join(outside, "implementer")); !os.IsNotExist(err) {
		t.Fatalf("symlink target was modified: %v", err)
	}
}

func TestGoalScratchRejectsFileSquat(t *testing.T) {
	artifactDir := t.TempDir()
	root, implementer := goalScratchPaths(artifactDir, "123e4567-e89b-42d3-a456-426614174000")
	if err := os.WriteFile(root, []byte("occupied"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := NewGoalStore()
	store.artifactDir, store.goalID = artifactDir, "123e4567-e89b-42d3-a456-426614174000"
	store.prepareScratchLocked()
	if store.scratchReady || store.scratchDir != implementer {
		t.Fatalf("scratch=%q ready=%v", store.scratchDir, store.scratchReady)
	}
	if info, err := os.Lstat(root); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("occupied root changed: info=%#v err=%v", info, err)
	}
}

func TestGoalBudgetLimitCleansScratch(t *testing.T) {
	registry := newPersistentGoalRegistry(t, t.TempDir(), filepath.Join(t.TempDir(), "artifacts"))
	defer registry.Close()
	if err := registry.BeginGoalWithBudget("bounded goal", 1); err != nil {
		t.Fatal(err)
	}
	scratch, ready := registry.GoalScratch()
	if !ready {
		t.Fatal("goal scratch was not ready")
	}
	registry.AddGoalTokens(1)
	if _, limited := registry.EnforceGoalBudget(); !limited {
		t.Fatal("goal budget was not enforced")
	}
	if _, err := os.Lstat(filepath.Dir(scratch)); !os.IsNotExist(err) {
		t.Fatalf("budget-limited goal retained scratch root: %v", err)
	}
}
