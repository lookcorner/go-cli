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

type recordingPlanObserver struct {
	entered  []PlanModeEvent
	exited   []PlanModeEvent
	decision PlanModeDecision
}

func (o *recordingPlanObserver) PlanModeEntered(event PlanModeEvent) {
	o.entered = append(o.entered, event)
}

func (o *recordingPlanObserver) ApprovePlanModeExit(context.Context, PlanModeEvent) (PlanModeDecision, error) {
	return o.decision, nil
}

func (o *recordingPlanObserver) PlanModeExited(event PlanModeEvent) {
	o.exited = append(o.exited, event)
}

func TestPlanModeToolsSeedGateApproveAndRestore(t *testing.T) {
	root := t.TempDir()
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	mode := NewPlanMode(ws, PromptApprover{Mode: PermissionAuto})
	stateDir := t.TempDir()
	if err := mode.Configure(stateDir); err != nil {
		t.Fatal(err)
	}
	observer := &recordingPlanObserver{decision: PlanModeDecision{Outcome: "approved"}}
	mode.SetObserver(observer)
	ctx := WithToolCall(context.Background(), "enter-1", "enter_plan_mode")
	output, err := (&enterPlanModeTool{mode: mode}).Execute(ctx, nil)
	if err != nil || !mode.Active() || !strings.Contains(output, "planFilePath") || len(observer.entered) != 1 || observer.entered[0].ToolCallID != "enter-1" {
		t.Fatalf("output=%s active=%v entered=%#v err=%v", output, mode.Active(), observer.entered, err)
	}
	planPath := filepath.Join(root, ".grok", "plan.md")
	if data, err := os.ReadFile(planPath); err != nil || len(data) != 0 {
		t.Fatalf("seeded plan=%q err=%v", data, err)
	}
	if err := mode.Allow("write_file", json.RawMessage(`{"path":"main.go"}`), nil); err == nil {
		t.Fatal("plan mode allowed an ordinary workspace edit")
	}
	if err := mode.Allow("write_file", json.RawMessage(`{"path":".grok/plan.md"}`), nil); err != nil {
		t.Fatalf("plan file edit rejected: %v", err)
	}
	if err := mode.Allow("run_terminal_cmd", nil, nil); err == nil {
		t.Fatal("plan mode allowed terminal execution")
	}
	if err := os.WriteFile(planPath, []byte("# Plan\n\n1. Change the parser.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	exitOutput, err := (&exitPlanModeTool{mode: mode}).Execute(WithToolCall(context.Background(), "exit-1", "exit_plan_mode"), nil)
	if err != nil || mode.Active() || !strings.Contains(exitOutput, "Change the parser") || len(observer.exited) != 1 || observer.exited[0].ToolCallID != "exit-1" {
		t.Fatalf("output=%s active=%v exited=%#v err=%v", exitOutput, mode.Active(), observer.exited, err)
	}
	restored := NewPlanMode(ws, PromptApprover{Mode: PermissionAuto})
	if err := restored.Configure(stateDir); err != nil || restored.Active() {
		t.Fatalf("restored active=%v err=%v", restored.Active(), err)
	}
}

func TestPlanModeCancelledExitStaysActive(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mode := NewPlanMode(ws, PromptApprover{Mode: PermissionAuto})
	mode.SetObserver(&recordingPlanObserver{decision: PlanModeDecision{Outcome: "cancelled", Feedback: "add rollback steps"}})
	if err := mode.SetActive(true); err != nil {
		t.Fatal(err)
	}
	_, err = (&exitPlanModeTool{mode: mode}).Execute(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "add rollback steps") || !mode.Active() {
		t.Fatalf("err=%v active=%v", err, mode.Active())
	}
}

func TestEnterPlanModeNeverTruncatesExistingPlan(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".grok"), 0o700); err != nil {
		t.Fatal(err)
	}
	plan := filepath.Join(root, ".grok", "plan.md")
	if err := os.WriteFile(plan, []byte("existing plan"), 0o600); err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	mode := NewPlanMode(ws, PromptApprover{Mode: PermissionAuto})
	if _, err := (&enterPlanModeTool{mode: mode}).Execute(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(plan); string(data) != "existing plan" {
		t.Fatalf("existing plan was changed: %q", data)
	}
}

func TestRegistryEnforcesPlanModeWriteGate(t *testing.T) {
	root := t.TempDir()
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry(ws, PromptApprover{Mode: PermissionAuto})
	defer registry.Close()
	if _, err := registry.Execute(context.Background(), "enter_plan_mode", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("enter plan mode: %v", err)
	}
	if _, err := registry.Execute(context.Background(), "write_file", json.RawMessage(`{"path":"main.go","content":"package main"}`)); err == nil {
		t.Fatal("registry allowed an ordinary workspace edit in plan mode")
	}
	if _, err := registry.Execute(context.Background(), "write_file", json.RawMessage(`{"path":".grok/plan.md","content":"# Plan\n"}`)); err != nil {
		t.Fatalf("registry rejected plan file edit: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(root, ".grok", "plan.md")); err != nil || string(data) != "# Plan\n" {
		t.Fatalf("plan file=%q err=%v", data, err)
	}
}
