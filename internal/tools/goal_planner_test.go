package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testGoalPlan = "# Plan: ship the feature\n\n## Goal kind\ncode-change\n\n## Acceptance criteria\n1. The feature works.\n\n## Verification plan\n1. Run tests.\n\n## Non-goals\n- Unrequested work.\n\n## Assumed scope\nCurrent workspace.\n\n## Implementation approach\nKeep it small.\n\n## Task checklist\n- [ ] Implement and verify."

func TestGoalPlannerPersistsPrivatePlanAndRunsOnce(t *testing.T) {
	root, artifactDir := t.TempDir(), filepath.Join(t.TempDir(), "artifacts")
	registry := newPersistentGoalRegistry(t, root, artifactDir)
	backend := &goalVerifierBackend{outputs: []string{testGoalPlan}}
	registry.subagents.set(backend)
	registry.ConfigureGoalRoles(GoalRoleConfig{
		PlannerEnabled: true,
		Planner:        GoalRoleModel{Model: "planner-model", AgentType: "plan"},
	})
	if err := registry.BeginGoal("ship the feature"); err != nil {
		t.Fatal(err)
	}
	path, err := registry.RunGoalPlanner(context.Background())
	if err != nil || path != filepath.Join(artifactDir, "goal-plan.md") {
		t.Fatalf("path=%q err=%v", path, err)
	}
	request := backend.requests[0]
	if request.Model != "planner-model" || request.HarnessType != "plan" || request.Type != "general-purpose" || request.CapabilityMode != "execute" || request.CWD != registry.goal.workspaceRoot {
		t.Fatalf("request=%#v", request)
	}
	if !strings.Contains(request.Prompt, "3-8 ordered `- [ ]` steps") || !strings.Contains(request.Prompt, "Do not put checkboxes in other sections") || !strings.Contains(request.Prompt, "literal `{SCRATCH}` placeholder") {
		t.Fatalf("planner prompt lacks next-step contract:\n%s", request.Prompt)
	}
	for _, target := range []string{path, registry.goal.planBaselinePath} {
		data, readErr := os.ReadFile(target)
		info, statErr := os.Stat(target)
		if readErr != nil || statErr != nil || strings.TrimSpace(string(data)) != testGoalPlan || info.Mode().Perm() != 0o600 {
			t.Fatalf("artifact=%q data=%q read=%v stat=%v mode=%v", target, data, readErr, statErr, info)
		}
	}
	if again, err := registry.RunGoalPlanner(context.Background()); err != nil || again != path || len(backend.requests) != 1 {
		t.Fatalf("again=%q err=%v requests=%d", again, err, len(backend.requests))
	}
	registry.Close()

	restored := newPersistentGoalRegistry(t, root, artifactDir)
	defer restored.Close()
	if restored.GoalSnapshot().Status != "user_paused" {
		t.Fatalf("restored status=%q", restored.GoalSnapshot().Status)
	}
	if _, err := restored.ResumeGoal(); err != nil {
		t.Fatal(err)
	}
	restoredBackend := &goalVerifierBackend{}
	restored.subagents.set(restoredBackend)
	restored.ConfigureGoalRoles(GoalRoleConfig{PlannerEnabled: true})
	if got, err := restored.RunGoalPlanner(context.Background()); err != nil || got != path || len(restoredBackend.requests) != 0 || restored.GoalSnapshot().PlanPath != path {
		t.Fatalf("restored path=%q err=%v requests=%d snapshot=%#v", got, err, len(restoredBackend.requests), restored.GoalSnapshot())
	}
	evidence := restored.goal.captureEvidence(context.Background(), 1)
	if evidence.planPath != path || evidence.planChanges != "" {
		t.Fatalf("evidence path=%q changes=%q", evidence.planPath, evidence.planChanges)
	}
}

func TestGoalPlannerRetryPreservesFirstSuccessfulBaseline(t *testing.T) {
	registry := &Registry{subagents: &subagentHolder{}, goal: NewGoalStore()}
	registry.goal.artifactDir = t.TempDir()
	registry.ConfigureGoalRoles(GoalRoleConfig{PlannerEnabled: true})
	backend := &goalVerifierBackend{outputs: []string{testGoalPlan, "# Plan: replacement\n"}}
	registry.subagents.set(backend)
	if err := registry.BeginGoal("goal"); err != nil {
		t.Fatal(err)
	}
	planPath, err := registry.RunGoalPlanner(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	baselinePath := registry.goal.planBaselinePath
	if err := os.Remove(planPath); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.RunGoalPlanner(context.Background()); err != nil {
		t.Fatal(err)
	}
	baseline, err := os.ReadFile(baselinePath)
	if err != nil || strings.TrimSpace(string(baseline)) != testGoalPlan {
		t.Fatalf("baseline=%q err=%v", baseline, err)
	}
	current, err := os.ReadFile(planPath)
	if err != nil || strings.TrimSpace(string(current)) != "# Plan: replacement" || len(backend.requests) != 2 {
		t.Fatalf("current=%q err=%v requests=%d", current, err, len(backend.requests))
	}
}

func TestGoalPlannerRetriesRoleAndFailClosedResumeRetries(t *testing.T) {
	t.Run("role fallback", func(t *testing.T) {
		registry := &Registry{subagents: &subagentHolder{}, goal: NewGoalStore()}
		registry.goal.artifactDir = t.TempDir()
		registry.ConfigureGoalRoles(GoalRoleConfig{
			PlannerEnabled: true,
			Planner:        GoalRoleModel{Model: "missing", AgentType: "plan"},
		})
		backend := &goalVerifierBackend{outputs: []string{"", testGoalPlan}, errors: []error{errors.New("role unavailable")}}
		registry.subagents.set(backend)
		if err := registry.BeginGoal("goal"); err != nil {
			t.Fatal(err)
		}
		if _, err := registry.RunGoalPlanner(context.Background()); err != nil || len(backend.requests) != 2 || backend.requests[1].Model != "" || backend.requests[1].HarnessType != "" {
			t.Fatalf("err=%v requests=%#v", err, backend.requests)
		}
	})

	t.Run("cancellation does not retry", func(t *testing.T) {
		registry := &Registry{subagents: &subagentHolder{}, goal: NewGoalStore()}
		registry.goal.artifactDir = t.TempDir()
		registry.ConfigureGoalRoles(GoalRoleConfig{
			PlannerEnabled: true,
			Planner:        GoalRoleModel{Model: "planner", AgentType: "plan"},
		})
		backend := &goalVerifierBackend{outputs: []string{""}, errors: []error{context.Canceled}}
		registry.subagents.set(backend)
		if err := registry.BeginGoal("goal"); err != nil {
			t.Fatal(err)
		}
		if _, err := registry.RunGoalPlanner(context.Background()); !errors.Is(err, context.Canceled) || len(backend.requests) != 1 {
			t.Fatalf("err=%v requests=%d", err, len(backend.requests))
		}
		if snapshot := registry.GoalSnapshot(); snapshot.Status != "user_paused" {
			t.Fatalf("snapshot=%#v", snapshot)
		}
	})

	t.Run("resume after failure", func(t *testing.T) {
		registry := &Registry{subagents: &subagentHolder{}, goal: NewGoalStore()}
		registry.goal.artifactDir = t.TempDir()
		registry.ConfigureGoalRoles(GoalRoleConfig{PlannerEnabled: true})
		backend := &goalVerifierBackend{outputs: []string{"", testGoalPlan}, errors: []error{errors.New("offline"), nil}}
		registry.subagents.set(backend)
		if err := registry.BeginGoal("goal"); err != nil {
			t.Fatal(err)
		}
		if _, err := registry.RunGoalPlanner(context.Background()); err == nil {
			t.Fatal("planner failure was accepted")
		}
		if snapshot := registry.GoalSnapshot(); snapshot.Status != "user_paused" || snapshot.Message != goalPlannerFailure {
			t.Fatalf("failed snapshot=%#v", snapshot)
		}
		if _, err := registry.ResumeGoal(); err != nil {
			t.Fatal(err)
		}
		path, err := registry.RunGoalPlanner(context.Background())
		if err != nil || path == "" || registry.GoalSnapshot().Status != "active" || len(backend.requests) != 2 {
			t.Fatalf("retry path=%q err=%v requests=%d snapshot=%#v", path, err, len(backend.requests), registry.GoalSnapshot())
		}
	})
}

func TestGoalPlannerDisabledTracksNoPlan(t *testing.T) {
	registry := &Registry{subagents: &subagentHolder{}, goal: NewGoalStore()}
	backend := &goalVerifierBackend{}
	registry.subagents.set(backend)
	registry.ConfigureGoalRoles(GoalRoleConfig{})
	if err := registry.BeginGoal("goal"); err != nil {
		t.Fatal(err)
	}
	if path, err := registry.RunGoalPlanner(context.Background()); err != nil || path != "" || len(backend.requests) != 0 || registry.GoalSnapshot().Status != "active" {
		t.Fatalf("path=%q err=%v requests=%d snapshot=%#v", path, err, len(backend.requests), registry.GoalSnapshot())
	}
}

func TestGoalPlannerCurrentModelKillSwitch(t *testing.T) {
	registry := &Registry{subagents: &subagentHolder{}, goal: NewGoalStore()}
	registry.goal.artifactDir = t.TempDir()
	backend := &goalVerifierBackend{outputs: []string{testGoalPlan}}
	registry.subagents.set(backend)
	registry.ConfigureGoalRoles(GoalRoleConfig{
		PlannerEnabled: true, UseCurrentModelOnly: true,
		Planner: GoalRoleModel{Model: "ignored", AgentType: "plan"},
	})
	if err := registry.BeginGoal("goal"); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.RunGoalPlanner(context.Background()); err != nil {
		t.Fatal(err)
	}
	if backend.requests[0].Model != "" || backend.requests[0].HarnessType != "" {
		t.Fatalf("request=%#v", backend.requests[0])
	}
}

func TestGoalPlannerMissingArtifactDirectoryFailsClosed(t *testing.T) {
	registry := &Registry{subagents: &subagentHolder{}, goal: NewGoalStore()}
	registry.ConfigureGoalRoles(GoalRoleConfig{PlannerEnabled: true})
	if err := registry.BeginGoal("goal"); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.RunGoalPlanner(context.Background()); err == nil {
		t.Fatal("missing artifact directory was accepted")
	}
	if snapshot := registry.GoalSnapshot(); snapshot.Status != "user_paused" || snapshot.Message != goalPlannerFailure {
		t.Fatalf("snapshot=%#v", snapshot)
	}
}
