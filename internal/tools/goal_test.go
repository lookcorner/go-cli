package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

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
	if err != nil || !strings.Contains(completed, "all checks passed") {
		t.Fatalf("unexpected completion result=%q err=%v", completed, err)
	}
	snapshot := store.Snapshot()
	if snapshot.Status != "completed" || snapshot.Message != "all checks passed" {
		t.Fatalf("unexpected snapshot: %#v", snapshot)
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
