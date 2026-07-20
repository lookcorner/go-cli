package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

type goalVerifierBackend struct {
	mu       sync.Mutex
	outputs  []string
	errors   []error
	requests []SubagentRequest
}

func (b *goalVerifierBackend) Description() string { return "goal verifier fixture" }
func (b *goalVerifierBackend) Start(_ context.Context, request SubagentRequest) (SubagentResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	index := len(b.requests)
	b.requests = append(b.requests, request)
	if index < len(b.errors) && b.errors[index] != nil {
		return SubagentResult{}, b.errors[index]
	}
	return SubagentResult{ID: fmt.Sprintf("skeptic-%d", index+1), Status: "completed", Output: b.outputs[index]}, nil
}
func (*goalVerifierBackend) Has(string) bool { return false }
func (*goalVerifierBackend) Output(context.Context, string, time.Duration) (SubagentResult, error) {
	return SubagentResult{}, errors.New("unused")
}
func (*goalVerifierBackend) Kill(context.Context, string) (string, error) { return "not_found", nil }

func TestUpdateGoalLifecycle(t *testing.T) {
	store := NewGoalStore()
	tool := &updateGoalTool{store: store}
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"message":"not active"}`)); err == nil {
		t.Fatal("expected inactive goal rejection")
	}
	if err := store.Begin("finish the implementation"); err != nil {
		t.Fatal(err)
	}
	progress, err := tool.Execute(context.Background(), json.RawMessage(`{"message":"tests are running"}`))
	if err != nil || !strings.Contains(progress, "Progress recorded") {
		t.Fatalf("unexpected progress result=%q err=%v", progress, err)
	}
	completed, err := tool.Execute(context.Background(), json.RawMessage(`{"completed":true,"message":"all checks passed"}`))
	if err != nil || !strings.Contains(completed, "Awaiting independent verification") {
		t.Fatalf("unexpected completion result=%q err=%v", completed, err)
	}
	snapshot := store.Snapshot()
	if snapshot.Status != "verifying" || snapshot.Message != "all checks passed" {
		t.Fatalf("unexpected snapshot: %#v", snapshot)
	}
	if err := store.StartVerification(10); err != nil {
		t.Fatal(err)
	}
	if err := store.ResolveVerification(true, "verified", 10); err != nil {
		t.Fatal(err)
	}
	snapshot = store.Snapshot()
	if snapshot.Status != "completed" || snapshot.Message != "verified" {
		t.Fatalf("unexpected verified snapshot: %#v", snapshot)
	}
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"message":"too late"}`)); err == nil {
		t.Fatal("expected terminal goal rejection")
	}
}

func TestUpdateGoalRejectsConflictingTerminalStates(t *testing.T) {
	store := NewGoalStore()
	if err := store.Begin("goal"); err != nil {
		t.Fatal(err)
	}
	tool := &updateGoalTool{store: store}
	if _, err := tool.Execute(context.Background(), json.RawMessage(
		`{"completed":true,"blocked_reason":"also blocked"}`,
	)); err == nil {
		t.Fatal("expected conflicting state rejection")
	}
}

func TestGoalVerifierMajorityRefutesAndReturnsGoalToActive(t *testing.T) {
	backend := &goalVerifierBackend{outputs: []string{
		`{"verdict":"refuted","gaps":"missing integration test"}`,
		`{"verdict":"not_refuted","gaps":""}`,
		`{"verdict":"refuted","gaps":"remote state was not checked"}`,
	}}
	registry := &Registry{subagents: &subagentHolder{}, goal: NewGoalStore()}
	registry.subagents.set(backend)
	if err := registry.BeginGoal("ship a verified implementation"); err != nil {
		t.Fatal(err)
	}
	if _, err := (&updateGoalTool{store: registry.goal}).Execute(context.Background(), json.RawMessage(`{"completed":true,"message":"done"}`)); err != nil {
		t.Fatal(err)
	}
	if err := registry.StartGoalVerification(10); err != nil {
		t.Fatal(err)
	}
	verification := registry.VerifyGoal(context.Background(), registry.GoalSnapshot(), 3)
	if verification.Achieved || verification.Summary != "missing integration test; remote state was not checked" {
		t.Fatalf("verification=%#v", verification)
	}
	if err := registry.ResolveGoalVerification(verification, 10); err != nil {
		t.Fatal(err)
	}
	if snapshot := registry.GoalSnapshot(); snapshot.Status != "active" || snapshot.Message != verification.Summary {
		t.Fatalf("snapshot=%#v", snapshot)
	}
	for _, request := range backend.requests {
		if request.Background || !request.BackgroundSet || request.CapabilityMode != "read-only" || request.Type != "general-purpose" {
			t.Fatalf("request=%#v", request)
		}
	}
}

func TestGoalVerificationPausesAtCapAndOnRepeatedGaps(t *testing.T) {
	complete := func(t *testing.T, store *GoalStore) {
		t.Helper()
		if _, err := (&updateGoalTool{store: store}).Execute(context.Background(), json.RawMessage(`{"completed":true}`)); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("cap", func(t *testing.T) {
		store := NewGoalStore()
		if err := store.Begin("goal"); err != nil {
			t.Fatal(err)
		}
		complete(t, store)
		if err := store.StartVerification(1); err != nil {
			t.Fatal(err)
		}
		if err := store.ResolveVerification(false, "missing proof", 1); err != nil {
			t.Fatal(err)
		}
		snapshot := store.Snapshot()
		if snapshot.Status != "paused" || snapshot.VerificationRuns != 1 || !strings.Contains(snapshot.Message, "run cap") {
			t.Fatalf("snapshot=%#v", snapshot)
		}
	})
	t.Run("no progress", func(t *testing.T) {
		store := NewGoalStore()
		if err := store.Begin("goal"); err != nil {
			t.Fatal(err)
		}
		for attempt := 0; attempt < 2; attempt++ {
			complete(t, store)
			if err := store.StartVerification(10); err != nil {
				t.Fatal(err)
			}
			if err := store.ResolveVerification(false, "same gap", 10); err != nil {
				t.Fatal(err)
			}
		}
		snapshot := store.Snapshot()
		if snapshot.Status != "paused" || snapshot.VerificationRuns != 2 || !strings.Contains(snapshot.Message, "no progress") {
			t.Fatalf("snapshot=%#v", snapshot)
		}
	})
}

func TestGoalVerifierCountsInfrastructureErrorsAsRefutations(t *testing.T) {
	backend := &goalVerifierBackend{
		outputs: []string{`{"verdict":"not_refuted","gaps":""}`, "", ""},
		errors:  []error{nil, errors.New("offline"), errors.New("offline")},
	}
	registry := &Registry{subagents: &subagentHolder{}}
	registry.subagents.set(backend)
	verification := registry.VerifyGoal(context.Background(), GoalSnapshot{Objective: "goal", Message: "candidate"}, 3)
	if verification.Achieved || !strings.Contains(verification.Summary, "offline") {
		t.Fatalf("verification=%#v", verification)
	}
}

func TestGoalVerifierUnavailableFailsOpen(t *testing.T) {
	registry := &Registry{subagents: &subagentHolder{}}
	verification := registry.VerifyGoal(context.Background(), GoalSnapshot{Objective: "goal", Message: "candidate"}, 3)
	if !verification.Achieved || !strings.Contains(verification.Summary, "accepted fail-open") {
		t.Fatalf("verification=%#v", verification)
	}
}

func TestGoalVerifierClampsSkepticCount(t *testing.T) {
	upper := &goalVerifierBackend{outputs: []string{
		`{"verdict":"not_refuted"}`, `{"verdict":"not_refuted"}`, `{"verdict":"not_refuted"}`,
		`{"verdict":"not_refuted"}`, `{"verdict":"not_refuted"}`,
	}}
	registry := &Registry{subagents: &subagentHolder{}}
	registry.subagents.set(upper)
	if verification := registry.VerifyGoal(context.Background(), GoalSnapshot{}, 99); !verification.Achieved || len(upper.requests) != 5 {
		t.Fatalf("upper verification=%#v requests=%d", verification, len(upper.requests))
	}
	lower := &goalVerifierBackend{outputs: []string{`{"verdict":"refuted","gaps":"missing"}`}}
	registry.subagents.set(lower)
	if verification := registry.VerifyGoal(context.Background(), GoalSnapshot{}, 0); verification.Achieved || len(lower.requests) != 1 {
		t.Fatalf("lower verification=%#v requests=%d", verification, len(lower.requests))
	}
}

func TestGoalVerifierResumesSkepticZeroAcrossRounds(t *testing.T) {
	backend := &goalVerifierBackend{outputs: []string{
		`{"verdict":"refuted","gaps":"missing proof"}`,
		`{"verdict":"not_refuted"}`,
		`{"verdict":"refuted","gaps":"missing proof"}`,
		`{"verdict":"not_refuted"}`,
		`{"verdict":"not_refuted"}`,
		`{"verdict":"not_refuted"}`,
	}}
	registry := &Registry{subagents: &subagentHolder{}, goal: NewGoalStore()}
	registry.subagents.set(backend)
	if err := registry.BeginGoal("goal"); err != nil {
		t.Fatal(err)
	}
	complete := func() {
		if _, err := (&updateGoalTool{store: registry.goal}).Execute(context.Background(), json.RawMessage(`{"completed":true}`)); err != nil {
			t.Fatal(err)
		}
		if err := registry.StartGoalVerification(10); err != nil {
			t.Fatal(err)
		}
	}
	complete()
	first := registry.VerifyGoal(context.Background(), registry.GoalSnapshot(), 3)
	if first.Achieved {
		t.Fatalf("first verification=%#v", first)
	}
	if err := registry.ResolveGoalVerification(first, 10); err != nil {
		t.Fatal(err)
	}
	complete()
	second := registry.VerifyGoal(context.Background(), registry.GoalSnapshot(), 3)
	if !second.Achieved {
		t.Fatalf("second verification=%#v", second)
	}

	backend.mu.Lock()
	requests := append([]SubagentRequest(nil), backend.requests...)
	backend.mu.Unlock()
	if len(requests) != 6 || requests[3].ResumeFrom != "skeptic-1" {
		t.Fatalf("requests=%#v", requests)
	}
	if !strings.Contains(requests[3].Prompt, "PRIOR GAPS:\nmissing proof") || !strings.Contains(requests[3].Prompt, "prior tool results may be stale") {
		t.Fatalf("resume prompt=%q", requests[3].Prompt)
	}
	if registry.goal.skeptic0SessionID != "skeptic-4" {
		t.Fatalf("skeptic0 session=%q", registry.goal.skeptic0SessionID)
	}
}

func TestGoalVerifierResumeFailureFallsBackCold(t *testing.T) {
	backend := &goalVerifierBackend{
		outputs: []string{"", `{"verdict":"not_refuted"}`, `{"verdict":"not_refuted"}`, `{"verdict":"not_refuted"}`},
		errors:  []error{errors.New("stale session")},
	}
	registry := &Registry{subagents: &subagentHolder{}, goal: NewGoalStore()}
	registry.subagents.set(backend)
	registry.goal.skeptic0SessionID = "prior-child"
	verification := registry.VerifyGoal(context.Background(), GoalSnapshot{Objective: "goal", VerificationRuns: 2}, 3)
	if !verification.Achieved {
		t.Fatalf("verification=%#v", verification)
	}
	backend.mu.Lock()
	requests := append([]SubagentRequest(nil), backend.requests...)
	backend.mu.Unlock()
	if len(requests) != 4 || requests[0].ResumeFrom != "prior-child" || requests[1].ResumeFrom != "" {
		t.Fatalf("requests=%#v", requests)
	}
	if strings.Contains(requests[1].Prompt, "prior tool results may be stale") {
		t.Fatalf("cold fallback reused resume prompt: %q", requests[1].Prompt)
	}
}

func TestGoalVerifierSingleSkepticNeverResumes(t *testing.T) {
	backend := &goalVerifierBackend{outputs: []string{`{"verdict":"not_refuted"}`}}
	registry := &Registry{subagents: &subagentHolder{}, goal: NewGoalStore()}
	registry.subagents.set(backend)
	registry.goal.skeptic0SessionID = "prior-child"
	if verification := registry.VerifyGoal(context.Background(), GoalSnapshot{Objective: "goal"}, 1); !verification.Achieved {
		t.Fatalf("verification=%#v", verification)
	}
	if backend.requests[0].ResumeFrom != "" || registry.goal.skeptic0SessionID != "" {
		t.Fatalf("request=%#v session=%q", backend.requests[0], registry.goal.skeptic0SessionID)
	}
}

func TestParseGoalVerdictIsStrict(t *testing.T) {
	for _, test := range []struct {
		output  string
		verdict string
	}{
		{`{"verdict":"not_refuted","gaps":""}`, "not_refuted"},
		{"```json\n{\"verdict\":\"refuted\",\"gaps\":\"missing evidence\"}\n```", "refuted"},
		{"Not Refuted!", "not_refuted"},
		{"looks good", "refuted"},
	} {
		if got := parseGoalVerdict(test.output); got.Verdict != test.verdict {
			t.Fatalf("parse %q=%#v", test.output, got)
		}
	}
	large := `{"verdict":"refuted","gaps":"` + strings.Repeat("x", goalVerifierGapMaxBytes+100) + `"}`
	if verdict := parseGoalVerdict(large); len(verdict.Gaps) > goalVerifierGapMaxBytes+len("... (truncated)") || !strings.HasSuffix(verdict.Gaps, "... (truncated)") {
		t.Fatalf("large verdict gap length=%d", len(verdict.Gaps))
	}
}
