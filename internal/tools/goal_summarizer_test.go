package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func completeVerifiedGoal(t *testing.T, registry *Registry, detailsPath string) GoalVerification {
	t.Helper()
	if err := registry.BeginGoal("ship a usable command"); err != nil {
		t.Fatal(err)
	}
	if _, err := (&updateGoalTool{store: registry.goal}).Execute(context.Background(), json.RawMessage(`{"completed":true,"message":"implemented"}`)); err != nil {
		t.Fatal(err)
	}
	if err := registry.StartGoalVerification(3); err != nil {
		t.Fatal(err)
	}
	verification := GoalVerification{Achieved: true, Verified: true, Summary: "verified", DetailsPath: detailsPath}
	if err := registry.ResolveGoalVerification(verification, 3); err != nil {
		t.Fatal(err)
	}
	return verification
}

func TestGoalSummarizerSurfacesOnceAndPersists(t *testing.T) {
	root, artifactDir := t.TempDir(), filepath.Join(t.TempDir(), "artifacts")
	planPath := filepath.Join(root, filepath.FromSlash(planFile))
	if err := os.MkdirAll(filepath.Dir(planPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(planPath, []byte("# Plan\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	registry := newPersistentGoalRegistry(t, root, artifactDir)
	backend := &goalVerifierBackend{outputs: []string{"Delivered the `gork` command.\n\n- Run `gork --help`."}}
	registry.subagents.set(backend)
	registry.ConfigureGoalRoles(GoalRoleConfig{SummaryEnabled: true})
	detailsPath := filepath.Join(artifactDir, "goal-classifier-1.md")
	if err := writeGoalArtifact(detailsPath, []byte("verified\n")); err != nil {
		t.Fatal(err)
	}
	verification := completeVerifiedGoal(t, registry, detailsPath)
	summary := registry.RunGoalSummarizer(context.Background(), verification)
	if !strings.Contains(summary, "Run `gork --help`") {
		t.Fatalf("summary=%q", summary)
	}
	request := backend.requests[0]
	if request.Description != "goal summarizer" || request.Type != "general-purpose" || request.CapabilityMode != "read-only" || request.Background || !request.BackgroundSet || request.Model != "" || request.HarnessType != "" {
		t.Fatalf("request=%#v", request)
	}
	for _, required := range []string{"ship a usable command", planPath, detailsPath, "SESSION_TRACES_DIR: (unavailable)", "80 words", "4 bullets"} {
		if !strings.Contains(request.Prompt, required) {
			t.Fatalf("prompt missing %q:\n%s", required, request.Prompt)
		}
	}
	if again := registry.RunGoalSummarizer(context.Background(), verification); again != "" || len(backend.requests) != 1 {
		t.Fatalf("again=%q requests=%d", again, len(backend.requests))
	}
	registry.Close()

	restored := newPersistentGoalRegistry(t, root, artifactDir)
	defer restored.Close()
	if snapshot := restored.GoalSnapshot(); snapshot.Status != "completed" || snapshot.ClosingSummary != summary || !restored.goal.summaryAttempted {
		t.Fatalf("snapshot=%#v attempted=%v", snapshot, restored.goal.summaryAttempted)
	}
}

func TestGoalSummarizerIsFailOpenAndGatedByRealVerification(t *testing.T) {
	for _, test := range []struct {
		name         string
		enabled      bool
		verification GoalVerification
		outputs      []string
		errors       []error
		wantRequests int
	}{
		{name: "runtime failure", enabled: true, verification: GoalVerification{Achieved: true, Verified: true}, outputs: []string{""}, errors: []error{errors.New("offline")}, wantRequests: 1},
		{name: "empty output", enabled: true, verification: GoalVerification{Achieved: true, Verified: true}, outputs: []string{"   "}, wantRequests: 1},
		{name: "disabled", verification: GoalVerification{Achieved: true, Verified: true}},
		{name: "whole verifier fail open", enabled: true, verification: GoalVerification{Achieved: true}},
		{name: "not achieved", enabled: true, verification: GoalVerification{Verified: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			registry := &Registry{subagents: &subagentHolder{}, goal: NewGoalStore()}
			registry.goal.artifactDir = t.TempDir()
			backend := &goalVerifierBackend{outputs: test.outputs, errors: test.errors}
			registry.subagents.set(backend)
			registry.ConfigureGoalRoles(GoalRoleConfig{SummaryEnabled: test.enabled})
			verification := completeVerifiedGoal(t, registry, "")
			verification.Achieved, verification.Verified = test.verification.Achieved, test.verification.Verified
			if summary := registry.RunGoalSummarizer(context.Background(), verification); summary != "" {
				t.Fatalf("summary=%q", summary)
			}
			if len(backend.requests) != test.wantRequests || registry.GoalSnapshot().Status != "completed" {
				t.Fatalf("requests=%d snapshot=%#v", len(backend.requests), registry.GoalSnapshot())
			}
		})
	}
}

func TestGoalSummarizerClipsRunes(t *testing.T) {
	registry := &Registry{subagents: &subagentHolder{}, goal: NewGoalStore()}
	registry.goal.artifactDir = t.TempDir()
	backend := &goalVerifierBackend{outputs: []string{strings.Repeat("x", goalSummaryMaxChars+1)}}
	registry.subagents.set(backend)
	registry.ConfigureGoalRoles(GoalRoleConfig{SummaryEnabled: true})
	verification := completeVerifiedGoal(t, registry, "")
	summary := registry.RunGoalSummarizer(context.Background(), verification)
	if len([]rune(summary)) != goalSummaryMaxChars+6 || !strings.HasSuffix(summary, " [...]") {
		t.Fatalf("summary runes=%d suffix=%q", len([]rune(summary)), summary[len(summary)-6:])
	}
}
