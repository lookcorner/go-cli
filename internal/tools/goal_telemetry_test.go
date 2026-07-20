package tools

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
)

type goalEventRecorder struct {
	mu     sync.Mutex
	events []GoalEvent
}

func (r *goalEventRecorder) GoalEvent(event GoalEvent) {
	r.mu.Lock()
	r.events = append(r.events, event)
	r.mu.Unlock()
}

func (r *goalEventRecorder) matching(kind string) []GoalEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	var result []GoalEvent
	for _, event := range r.events {
		if event.Kind == kind {
			result = append(result, event)
		}
	}
	return result
}

func TestGoalTelemetryRecordsLifecycleAndSuccessfulRoles(t *testing.T) {
	root, artifactDir := t.TempDir(), filepath.Join(t.TempDir(), "artifacts")
	registry := newPersistentGoalRegistry(t, root, artifactDir)
	defer registry.Close()
	recorder := &goalEventRecorder{}
	registry.SetGoalObserver(recorder)
	registry.ConfigureGoalRoles(GoalRoleConfig{
		CurrentModel: "parent-model", ClassifierMaxRuns: 4, PlannerEnabled: true, SummaryEnabled: true,
	})
	registry.subagents.set(&goalVerifierBackend{outputs: []string{testGoalPlan}})
	if err := registry.BeginGoal("ship telemetry"); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.RunGoalPlanner(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := (&updateGoalTool{store: registry.goal}).Execute(context.Background(), json.RawMessage(`{"completed":true,"message":"candidate"}`)); err != nil {
		t.Fatal(err)
	}
	if err := registry.StartGoalVerification(4); err != nil {
		t.Fatal(err)
	}
	registry.subagents.set(&goalVerifierBackend{outputs: []string{
		`{"verdict":"not_refuted"}`, `{"verdict":"not_refuted"}`, `{"verdict":"refuted","gaps":"minority"}`,
	}})
	verification := registry.VerifyGoal(context.Background(), registry.GoalSnapshot(), 3)
	if !verification.Achieved || !verification.Verified {
		t.Fatalf("verification=%#v", verification)
	}
	if err := registry.ResolveGoalVerification(verification, 4); err != nil {
		t.Fatal(err)
	}
	registry.subagents.set(&goalVerifierBackend{outputs: []string{"Delivered telemetry.\n\n- Run `gork --goal ...`."}})
	if summary := registry.RunGoalSummarizer(context.Background(), verification); summary == "" {
		t.Fatal("summary was not produced")
	}

	if events := recorder.matching("goal_planner_fired"); len(events) != 1 || events[0].Data["attempt"] != 1 || events[0].Data["max_runs"] != 1 || events[0].Data["model_id"] != "parent-model" {
		t.Fatalf("planner fired=%#v", events)
	}
	if len(recorder.matching("goal_planner_completed")) != 1 || len(recorder.matching("goal_verifier_skeptic_verdict")) != 3 {
		t.Fatalf("events=%#v", recorder.events)
	}
	classifier := recorder.matching("goal_classifier_fired")
	if len(classifier) != 1 || classifier[0].Data["attempt"] != uint32(1) || classifier[0].Data["max_runs"] != uint32(4) || classifier[0].Data["model_id"] != "parent-model" {
		t.Fatalf("classifier=%#v", classifier)
	}
	aggregate := recorder.matching("goal_verifier_aggregate_verdict")
	if len(aggregate) != 1 || aggregate[0].Data["refuted_count"] != 1 || aggregate[0].Data["total"] != 3 || aggregate[0].Data["achieved"] != true {
		t.Fatalf("aggregate=%#v", aggregate)
	}
	verdict := recorder.matching("goal_classifier_verdict")
	if len(verdict) != 1 || verdict[0].Data["verdict"] != "achieved" || verdict[0].Data["attempt"] != uint32(1) {
		t.Fatalf("verdict=%#v", verdict)
	}
	if len(recorder.matching("goal_summarizer_fired")) != 1 || len(recorder.matching("goal_summarizer_completed")) != 1 {
		t.Fatalf("summary events=%#v", recorder.events)
	}
	updates := recorder.matching("goal_updated")
	if len(updates) < 6 || updates[0].Data["last_event"] != "goal_created" || updates[len(updates)-1].Data["last_event"] != "goal_completed" {
		t.Fatalf("updates=%#v", updates)
	}
	planning := updates[1].Data
	if planning["last_event"] != "planning_started" || planning["phase"] != "planning" || planning["planning"] != true || planning["current_subagent_role"] != "planner" {
		t.Fatalf("planning update=%#v", planning)
	}
	verifying := false
	for _, update := range updates {
		if update.Data["last_event"] == "verification_started" && update.Data["current_subagent_role"] == "verifier" {
			verifying = true
		}
	}
	if !verifying {
		t.Fatalf("missing verifier role update: %#v", updates)
	}
	if last := updates[len(updates)-1].Data; last["status"] != "complete" || last["phase"] != "idle" || last["classifier_runs_attempted"] != uint32(1) || last["classifier_max_runs"] != uint32(4) || last["total_verify_rounds"] != uint32(1) {
		t.Fatalf("completed update=%#v", last)
	} else if _, exists := last["current_subagent_role"]; exists {
		t.Fatalf("completed update retained role=%#v", last)
	}
	if _, exists := updates[0].Data["classifier_runs_attempted"]; exists || updates[0].Data["last_event_timestamp"] == "" {
		t.Fatalf("created update=%#v", updates[0].Data)
	}
	created := updates[0].Data
	if created["goal_id"] == "" || created["total_worker_rounds"] != uint32(0) || created["total_verify_rounds"] != uint32(0) || created["token_baseline"] != int64(0) || created["finished_subagent_tokens"] != int64(0) {
		t.Fatalf("created wire fields=%#v", created)
	}
	if _, exists := created["current_subagent_role"]; exists {
		t.Fatalf("empty optional role was serialized: %#v", created)
	}
	if _, exists := created["deliverables"]; exists {
		t.Fatalf("empty optional deliverables were serialized: %#v", created)
	}
}

func TestGoalTelemetryRecordsClassifierCap(t *testing.T) {
	registry := &Registry{subagents: &subagentHolder{}, goal: NewGoalStore()}
	recorder := &goalEventRecorder{}
	registry.SetGoalObserver(recorder)
	if err := registry.BeginGoal("goal"); err != nil {
		t.Fatal(err)
	}
	if _, err := (&updateGoalTool{store: registry.goal}).Execute(context.Background(), json.RawMessage(`{"completed":true}`)); err != nil {
		t.Fatal(err)
	}
	if err := registry.StartGoalVerification(1); err != nil {
		t.Fatal(err)
	}
	if err := registry.ResolveGoalVerification(GoalVerification{Summary: "missing"}, 1); err != nil {
		t.Fatal(err)
	}
	events := recorder.matching("goal_classifier_cap_reached")
	paused := recorder.matching("goal_auto_paused")
	if len(events) != 1 || events[0].Data["attempt"] != uint32(1) || len(paused) != 1 || paused[0].Data["reason"] != "back_off" {
		t.Fatalf("events=%#v", recorder.events)
	}
}

func TestGoalBudgetTelemetryAndRoleTokenAccounting(t *testing.T) {
	registry := &Registry{subagents: &subagentHolder{}, goal: NewGoalStore()}
	registry.goal.artifactDir = t.TempDir()
	recorder := &goalEventRecorder{}
	registry.SetGoalObserver(recorder)
	registry.ConfigureGoalRoles(GoalRoleConfig{PlannerEnabled: true})
	registry.subagents.set(&goalVerifierBackend{outputs: []string{testGoalPlan}, tokens: []int{65}})
	if err := registry.BeginGoalWithBudget("bounded goal", 100); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.RunGoalPlanner(context.Background()); err != nil {
		t.Fatal(err)
	}
	updates := recorder.matching("goal_updated")
	if last := updates[len(updates)-2].Data; last["last_event"] != "tokens_updated" || last["tokens_used"] != int64(65) {
		t.Fatalf("live token update=%#v", last)
	}
	if err := registry.RecordGoalWorkerRound(); err != nil {
		t.Fatal(err)
	}
	registry.AddGoalTokens(40)
	snapshot, limited := registry.EnforceGoalBudget()
	if !limited || snapshot.TokensUsed != 105 || snapshot.TokenBudget != 100 || snapshot.FinishedSubagentTokens != 65 {
		t.Fatalf("limited=%v snapshot=%#v", limited, snapshot)
	}
	updates = recorder.matching("goal_updated")
	last := updates[len(updates)-1].Data
	if last["status"] != "budget_limited" || last["last_event"] != "budget_exceeded" || last["token_budget"] != int64(100) || last["tokens_used"] != int64(105) || last["finished_subagent_tokens"] != int64(65) || last["total_worker_rounds"] != uint32(1) {
		t.Fatalf("last update=%#v", last)
	}
}

func TestGoalTelemetryRecordsRoleFailures(t *testing.T) {
	t.Run("planner fallback then fail closed", func(t *testing.T) {
		registry := &Registry{subagents: &subagentHolder{}, goal: NewGoalStore()}
		registry.goal.artifactDir = t.TempDir()
		recorder := &goalEventRecorder{}
		registry.SetGoalObserver(recorder)
		registry.ConfigureGoalRoles(GoalRoleConfig{
			CurrentModel: "parent", PlannerEnabled: true,
			Planner: GoalRoleModel{Model: "role", AgentType: "plan"},
		})
		registry.subagents.set(&goalVerifierBackend{outputs: []string{"", ""}, errors: []error{errors.New("unavailable"), nil}})
		if err := registry.BeginGoal("goal"); err != nil {
			t.Fatal(err)
		}
		if _, err := registry.RunGoalPlanner(context.Background()); err == nil {
			t.Fatal("planner failure was accepted")
		}
		failed := recorder.matching("goal_planner_fail_closed")
		if len(recorder.matching("goal_role_model_fail_open")) != 1 || len(failed) != 1 || failed[0].Data["reason"] != "missing_plan_file" || len(recorder.matching("goal_auto_paused")) != 1 {
			t.Fatalf("events=%#v", recorder.events)
		}
	})

	t.Run("classifier infrastructure fail open", func(t *testing.T) {
		registry := &Registry{subagents: &subagentHolder{}, goal: NewGoalStore()}
		recorder := &goalEventRecorder{}
		registry.SetGoalObserver(recorder)
		registry.ConfigureGoalRoles(GoalRoleConfig{CurrentModel: "parent", ClassifierMaxRuns: 3})
		verification := registry.VerifyGoal(context.Background(), GoalSnapshot{VerificationRuns: 2}, 3)
		failed := recorder.matching("goal_classifier_fail_open")
		if !verification.Achieved || verification.Verified || len(failed) != 1 || failed[0].Data["reason"] != "sampler_error" {
			t.Fatalf("verification=%#v events=%#v", verification, recorder.events)
		}
	})

	t.Run("strategist and summarizer fail open", func(t *testing.T) {
		registry := &Registry{subagents: &subagentHolder{}, goal: NewGoalStore()}
		registry.goal.status, registry.goal.objective = "active", "goal"
		registry.goal.artifactDir, registry.goal.consecutiveReject, registry.goal.verificationRuns = t.TempDir(), 1, 2
		recorder := &goalEventRecorder{}
		registry.SetGoalObserver(recorder)
		registry.ConfigureGoalRoles(GoalRoleConfig{CurrentModel: "parent", StrategistEvery: 1, SummaryEnabled: true})
		registry.subagents.set(&goalVerifierBackend{outputs: []string{""}, errors: []error{errors.New("offline")}})
		if result := registry.RunGoalStrategist(context.Background()); result != "" {
			t.Fatalf("strategy=%q", result)
		}
		failed := recorder.matching("goal_strategist_failed")
		if len(failed) != 1 || failed[0].Data["reason"] != "runtime" || failed[0].Data["attempt"] != uint32(2) {
			t.Fatalf("events=%#v", recorder.events)
		}

		registry.goal.status, registry.goal.verificationRuns = "completed", 3
		registry.subagents.set(&goalVerifierBackend{outputs: []string{""}, errors: []error{context.Canceled}})
		registry.RunGoalSummarizer(context.Background(), GoalVerification{Achieved: true, Verified: true})
		summaryFailed := recorder.matching("goal_summarizer_fail_open")
		if len(summaryFailed) != 1 || summaryFailed[0].Data["reason"] != "aborted" || summaryFailed[0].Data["attempt"] != uint32(3) {
			t.Fatalf("events=%#v", recorder.events)
		}
	})
}
