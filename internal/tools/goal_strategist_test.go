package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGoalStrategistRunsAtCadenceProtectsPlanAndPersistsNote(t *testing.T) {
	root, artifactDir := t.TempDir(), filepath.Join(t.TempDir(), "artifacts")
	planPath := filepath.Join(root, filepath.FromSlash(planFile))
	if err := os.MkdirAll(filepath.Dir(planPath), 0o700); err != nil {
		t.Fatal(err)
	}
	const originalPlan = "# acceptance criteria\n"
	if err := os.WriteFile(planPath, []byte(originalPlan), 0o600); err != nil {
		t.Fatal(err)
	}
	backend := &goalVerifierBackend{
		outputs: []string{"# Strategy: why the goal is stuck and how to unstick it\n\n## Diagnosis\nTangled I/O.\n\n## Recommended restructure\n1. Extract a pure unit.\n\n## Why this converges\nIt becomes testable."},
		onStart: func(_ int, request SubagentRequest) {
			if request.Description == "goal strategist" {
				_ = os.WriteFile(planPath, []byte("corrupted"), 0o600)
			}
		},
	}
	registry := newPersistentGoalRegistry(t, root, artifactDir)
	defer registry.Close()
	registry.subagents.set(backend)
	registry.ConfigureGoalRoles(GoalRoleConfig{
		StrategistEvery: 2,
		Strategist:      GoalRoleModel{Model: "strategy-model", AgentType: "explore"},
	})
	registry.goal.status, registry.goal.objective = "active", "finish the feature"
	registry.goal.lastVerification, registry.goal.consecutiveReject = "different gaps", 2
	recommendation := registry.RunGoalStrategist(context.Background())
	if !strings.Contains(recommendation, "Extract a pure unit") {
		t.Fatalf("recommendation=%q", recommendation)
	}
	data, err := os.ReadFile(planPath)
	if err != nil || string(data) != originalPlan {
		t.Fatalf("plan=%q err=%v", data, err)
	}
	request := backend.requests[0]
	if request.CapabilityMode != "execute" || request.Model != "strategy-model" || request.HarnessType != "explore" || request.Type != "general-purpose" {
		t.Fatalf("request=%#v", request)
	}
	state := registry.goal
	if state.strategistBonus != goalStrategistBonus || state.strategyPath == "" || state.strategyNote == "" || state.verificationStall != 0 {
		t.Fatalf("strategist state bonus=%d path=%q note=%q stall=%d", state.strategistBonus, state.strategyPath, state.strategyNote, state.verificationStall)
	}
	info, err := os.Stat(state.strategyPath)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("strategy artifact mode=%v err=%v", info, err)
	}
	if again := registry.RunGoalStrategist(context.Background()); again != "" || len(backend.requests) != 1 {
		t.Fatalf("duplicate fire result=%q requests=%d", again, len(backend.requests))
	}
}

func TestGoalStrategistRoleFailureFallsBackAndTotalFailureRevokesBonus(t *testing.T) {
	registry := &Registry{subagents: &subagentHolder{}, goal: NewGoalStore()}
	registry.goal.status, registry.goal.objective = "active", "goal"
	registry.goal.artifactDir = t.TempDir()
	registry.goal.consecutiveReject = 1
	registry.ConfigureGoalRoles(GoalRoleConfig{
		StrategistEvery: 1,
		Strategist:      GoalRoleModel{Model: "missing", AgentType: "cursor"},
	})
	backend := &goalVerifierBackend{
		outputs: []string{"", "# Strategy\n\n## Diagnosis\nCause\n\n## Recommended restructure\n1. Fix structure\n\n## Why this converges\nProof"},
		errors:  []error{errors.New("role unavailable")},
	}
	registry.subagents.set(backend)
	if result := registry.RunGoalStrategist(context.Background()); result == "" || len(backend.requests) != 2 || backend.requests[1].Model != "" || backend.requests[1].HarnessType != "" {
		t.Fatalf("fallback result=%q requests=%#v", result, backend.requests)
	}

	registry.goal.consecutiveReject = 2
	backend.outputs = append(backend.outputs, "", "")
	backend.errors = append(backend.errors, errors.New("offline"), errors.New("offline"))
	if result := registry.RunGoalStrategist(context.Background()); result != "" || registry.goal.status != "active" || registry.goal.strategistBonus != 0 {
		t.Fatalf("fail-open result=%q status=%q bonus=%d", result, registry.goal.status, registry.goal.strategistBonus)
	}
}

func TestGoalStrategistBonusExtendsCapAndStallWindow(t *testing.T) {
	store := NewGoalStore()
	store.status, store.consecutiveReject = "active", 1
	if _, ok := store.claimStrategist(1); !ok || store.strategistBonus != goalStrategistBonus {
		t.Fatalf("claim ok=%v bonus=%d", ok, store.strategistBonus)
	}
	store.status, store.verificationRuns = "verifying", 2
	if err := store.StartVerification(2); err != nil || store.verificationRuns != 3 {
		t.Fatalf("extended cap runs=%d err=%v", store.verificationRuns, err)
	}
	store.verificationRuns = 1
	store.stallVerification, store.verificationStall = "same", 3
	if err := store.ResolveVerification(false, "same", 10); err != nil || store.status != "active" || store.verificationStall != 4 {
		t.Fatalf("relaxed stall status=%q count=%d err=%v", store.status, store.verificationStall, err)
	}
	store.status = "verifying"
	if err := store.ResolveVerification(false, "same", 10); err != nil || store.status != "paused" || store.verificationStall != 5 {
		t.Fatalf("bounded stall status=%q count=%d err=%v", store.status, store.verificationStall, err)
	}
}

func TestGoalPlanGuardRemovesStrategistCreatedPlan(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".grok", "plan.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	snapshot := captureGoalPlan(path)
	if err := os.WriteFile(path, []byte("created"), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot.restore()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("strategist-created plan survived: %v", err)
	}
}

func TestGoalPlanGuardRestoresDeletedPlan(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plan.md")
	if err := os.WriteFile(path, []byte("original"), 0o640); err != nil {
		t.Fatal(err)
	}
	snapshot := captureGoalPlan(path)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	snapshot.restore()
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "original" {
		t.Fatalf("restored plan=%q err=%v", data, err)
	}
}

func TestGoalStrategistKillSwitchUsesCurrentRole(t *testing.T) {
	backend := &goalVerifierBackend{outputs: []string{"# Strategy\n\n## Diagnosis\nCause\n\n## Recommended restructure\n1. Fix\n\n## Why this converges\nProof"}}
	registry := &Registry{subagents: &subagentHolder{}, goal: NewGoalStore()}
	registry.subagents.set(backend)
	registry.goal.status, registry.goal.objective = "active", "goal"
	registry.goal.artifactDir, registry.goal.consecutiveReject = t.TempDir(), 1
	registry.ConfigureGoalRoles(GoalRoleConfig{
		StrategistEvery: 1, UseCurrentModelOnly: true,
		Strategist: GoalRoleModel{Model: "ignored", AgentType: "explore"},
	})
	if result := registry.RunGoalStrategist(context.Background()); result == "" {
		t.Fatal("strategist did not run")
	}
	if backend.requests[0].Model != "" || backend.requests[0].HarnessType != "" {
		t.Fatalf("kill switch request=%#v", backend.requests[0])
	}
}
