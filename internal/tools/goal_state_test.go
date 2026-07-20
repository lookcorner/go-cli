package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lookcorner/go-cli/internal/workspace"
)

func newPersistentGoalRegistry(t *testing.T, root, artifactDir string) *Registry {
	t.Helper()
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry(ws, PromptApprover{Mode: PermissionAuto})
	if err := registry.ConfigureGoalVerification(artifactDir); err != nil {
		registry.Close()
		t.Fatal(err)
	}
	return registry
}

func TestGoalStatePersistsResumesAndCompletes(t *testing.T) {
	root, artifactDir := t.TempDir(), filepath.Join(t.TempDir(), "artifacts")
	first := newPersistentGoalRegistry(t, root, artifactDir)
	if err := first.BeginGoal("persist this objective"); err != nil {
		t.Fatal(err)
	}
	if _, err := (&updateGoalTool{store: first.goal}).Execute(context.Background(), json.RawMessage(`{"completed":true,"message":"candidate"}`)); err != nil {
		t.Fatal(err)
	}
	if err := first.StartGoalVerification(10); err != nil {
		t.Fatal(err)
	}
	if err := first.ResolveGoalVerification(GoalVerification{Summary: "missing proof"}, 10); err != nil {
		t.Fatal(err)
	}
	first.Close()

	statePath := filepath.Join(artifactDir, "goal.json")
	info, err := os.Stat(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("goal state mode=%v", info.Mode().Perm())
	}
	second := newPersistentGoalRegistry(t, root, artifactDir)
	snapshot := second.GoalSnapshot()
	if snapshot.Objective != "persist this objective" || snapshot.Status != "active" || snapshot.Message != "missing proof" || snapshot.VerificationRuns != 1 {
		t.Fatalf("restored snapshot=%#v", snapshot)
	}
	objective, err := second.ResumeGoal()
	if err != nil || objective != "persist this objective" {
		t.Fatalf("resume objective=%q err=%v", objective, err)
	}
	if snapshot = second.GoalSnapshot(); snapshot.Status != "active" || snapshot.Message != "" || snapshot.VerificationRuns != 0 {
		t.Fatalf("resumed snapshot=%#v", snapshot)
	}
	if _, err := (&updateGoalTool{store: second.goal}).Execute(context.Background(), json.RawMessage(`{"completed":true}`)); err != nil {
		t.Fatal(err)
	}
	if err := second.StartGoalVerification(10); err != nil {
		t.Fatal(err)
	}
	if err := second.ResolveGoalVerification(GoalVerification{Achieved: true, Summary: "verified"}, 10); err != nil {
		t.Fatal(err)
	}
	second.Close()

	third := newPersistentGoalRegistry(t, root, artifactDir)
	defer third.Close()
	if snapshot = third.GoalSnapshot(); snapshot.Status != "completed" || snapshot.Message != "verified" {
		t.Fatalf("completed snapshot=%#v", snapshot)
	}
	if _, err := third.ResumeGoal(); err == nil || !strings.Contains(err.Error(), "completed") {
		t.Fatalf("completed resume err=%v", err)
	}
}

func TestGoalStateUnknownStatusPausesAndBadVersionFails(t *testing.T) {
	root, artifactDir := t.TempDir(), filepath.Join(t.TempDir(), "artifacts")
	if err := os.Mkdir(artifactDir, 0o700); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(artifactDir, "goal.json")
	state := `{"version":1,"objective":"goal","status":"future_status","baseline_commit":"not-an-oid","plan_baseline_path":"/tmp/outside"}`
	if err := os.WriteFile(statePath, []byte(state), 0o600); err != nil {
		t.Fatal(err)
	}
	registry := newPersistentGoalRegistry(t, root, artifactDir)
	if snapshot := registry.GoalSnapshot(); snapshot.Status != "paused" || !strings.Contains(snapshot.Message, "unknown status") || registry.goal.baselineCommit != "" || registry.goal.planBaselinePath != "" {
		t.Fatalf("snapshot=%#v baseline=%q plan=%q", snapshot, registry.goal.baselineCommit, registry.goal.planBaselinePath)
	}
	registry.Close()
	if err := os.WriteFile(statePath, []byte(`{"version":2,"objective":"goal","status":"active"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	bad := NewRegistry(ws, PromptApprover{Mode: PermissionAuto})
	defer bad.Close()
	if err := bad.ConfigureGoalVerification(artifactDir); err == nil || !strings.Contains(err.Error(), "version") {
		t.Fatalf("bad version err=%v", err)
	}
}
