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
	if reminder, err := first.GoalReverifyReminder(8); err != nil || reminder != "" {
		t.Fatalf("reverify reminder=%q err=%v", reminder, err)
	}
	first.goal.recordSkeptic0Session("skeptic-child", false)
	first.goal.skepticModelAssignments([]GoalRoleModel{{Model: "skeptic-model", AgentType: "explore"}}, 1, false)
	strategyPath := filepath.Join(artifactDir, "goal-strategy.md")
	if err := writeGoalArtifact(strategyPath, []byte("# Strategy\n")); err != nil {
		t.Fatal(err)
	}
	if _, ok := first.goal.claimStrategist(1); !ok {
		t.Fatal("strategist was not claimed")
	}
	first.goal.resolveStrategist(strategyPath, "# Strategy", true)
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
	if snapshot.Objective != "persist this objective" || snapshot.Status != "active" || snapshot.Message != "missing proof" || snapshot.VerificationRuns != 1 || snapshot.RoundsSinceVerify != 1 {
		t.Fatalf("restored snapshot=%#v", snapshot)
	}
	if second.goal.skeptic0SessionID != "skeptic-child" || second.goal.lastVerification != "missing proof" || len(second.goal.skepticModels) != 1 || second.goal.skepticModels[0].Model != "skeptic-model" {
		t.Fatalf("restored skeptic=%q gap=%q models=%#v", second.goal.skeptic0SessionID, second.goal.lastVerification, second.goal.skepticModels)
	}
	if second.goal.consecutiveReject != 1 || second.goal.strategistFiredAt != 1 || second.goal.strategistBonus != goalStrategistBonus || second.goal.strategyPath != strategyPath || second.goal.strategyNote != "# Strategy" {
		t.Fatalf("restored strategist rejects=%d fired=%d bonus=%d path=%q note=%q", second.goal.consecutiveReject, second.goal.strategistFiredAt, second.goal.strategistBonus, second.goal.strategyPath, second.goal.strategyNote)
	}
	objective, err := second.ResumeGoal()
	if err != nil || objective != "persist this objective" {
		t.Fatalf("resume objective=%q err=%v", objective, err)
	}
	if snapshot = second.GoalSnapshot(); snapshot.Status != "active" || snapshot.Message != "" || snapshot.VerificationRuns != 0 {
		t.Fatalf("resumed snapshot=%#v", snapshot)
	}
	if second.goal.skeptic0SessionID != "skeptic-child" || second.goal.lastVerification != "missing proof" {
		t.Fatalf("resumed skeptic=%q gap=%q", second.goal.skeptic0SessionID, second.goal.lastVerification)
	}
	if second.goal.consecutiveReject != 0 || second.goal.strategistBonus != 0 || second.goal.strategyPath != "" || second.goal.strategyNote != "" {
		t.Fatalf("resumed strategist rejects=%d bonus=%d path=%q note=%q", second.goal.consecutiveReject, second.goal.strategistBonus, second.goal.strategyPath, second.goal.strategyNote)
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
	if third.goal.skeptic0SessionID != "" {
		t.Fatalf("completed skeptic session=%q", third.goal.skeptic0SessionID)
	}
	if len(third.goal.skepticModels) != 0 {
		t.Fatalf("completed skeptic models=%#v", third.goal.skepticModels)
	}
	if _, err := third.ResumeGoal(); err == nil || !strings.Contains(err.Error(), "completed") {
		t.Fatalf("completed resume err=%v", err)
	}
}

func TestGoalBudgetPersistsEnforcesAndCannotResume(t *testing.T) {
	root, artifactDir := t.TempDir(), filepath.Join(t.TempDir(), "artifacts")
	registry := newPersistentGoalRegistry(t, root, artifactDir)
	if err := registry.BeginGoalWithBudget("bounded work", 100); err != nil {
		t.Fatal(err)
	}
	registry.AddGoalTokens(60)
	registry.AddGoalTokens(50)
	if snapshot, limited := registry.EnforceGoalBudget(); !limited || snapshot.Status != "budget_limited" || snapshot.TokenBudget != 100 || snapshot.TokensUsed != 110 {
		t.Fatalf("limited=%v snapshot=%#v", limited, snapshot)
	}
	if err := registry.Close(); err != nil {
		t.Fatal(err)
	}

	restored := newPersistentGoalRegistry(t, root, artifactDir)
	defer restored.Close()
	if snapshot := restored.GoalSnapshot(); snapshot.Status != "budget_limited" || snapshot.TokenBudget != 100 || snapshot.TokensUsed != 110 {
		t.Fatalf("restored snapshot=%#v", snapshot)
	}
	if _, err := restored.ResumeGoal(); err == nil || !strings.Contains(err.Error(), "budget-limited") {
		t.Fatalf("resume err=%v", err)
	}
}

func TestGoalStateUnknownStatusPausesAndBadVersionFails(t *testing.T) {
	root, artifactDir := t.TempDir(), filepath.Join(t.TempDir(), "artifacts")
	if err := os.Mkdir(artifactDir, 0o700); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(artifactDir, "goal.json")
	state := `{"version":1,"objective":"goal","status":"future_status","baseline_commit":"not-an-oid","plan_baseline_path":"/tmp/outside","skeptic0_session_id":"../../bad"}`
	if err := os.WriteFile(statePath, []byte(state), 0o600); err != nil {
		t.Fatal(err)
	}
	registry := newPersistentGoalRegistry(t, root, artifactDir)
	if snapshot := registry.GoalSnapshot(); snapshot.Status != "paused" || !strings.Contains(snapshot.Message, "unknown status") || registry.goal.baselineCommit != "" || registry.goal.planBaselinePath != "" || registry.goal.skeptic0SessionID != "" {
		t.Fatalf("snapshot=%#v baseline=%q plan=%q skeptic=%q", snapshot, registry.goal.baselineCommit, registry.goal.planBaselinePath, registry.goal.skeptic0SessionID)
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
