package tools

import (
	"context"
	"encoding/json"
	"errors"
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
	return SubagentResult{Status: "completed", Output: b.outputs[index]}, nil
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
	if err := store.ResolveVerification(true, "verified"); err != nil {
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
	verification := registry.VerifyGoal(context.Background(), registry.GoalSnapshot(), 3)
	if verification.Achieved || !strings.Contains(verification.Summary, "missing integration test") || !strings.Contains(verification.Summary, "remote state") {
		t.Fatalf("verification=%#v", verification)
	}
	if err := registry.ResolveGoalVerification(verification); err != nil {
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
}
