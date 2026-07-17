package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestTodoWriteReplaceMergeAndPartialUpdate(t *testing.T) {
	tool := &todoWriteTool{store: newTodoStore()}
	output, err := tool.Execute(context.Background(), json.RawMessage(`{
		"merge": false,
		"todos": [
			{"id":"inspect","content":"Inspect repository","status":"completed"},
			{"id":"build","content":"Build implementation","status":"in_progress"}
		]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "[completed] inspect") || !strings.Contains(output, "[in_progress] build") {
		t.Fatalf("unexpected todo state: %s", output)
	}
	output, err = tool.Execute(context.Background(), json.RawMessage(`{
		"merge": true,
		"todos": [{"id":"build","status":"completed"}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "[completed] build: Build implementation") {
		t.Fatalf("partial update lost content: %s", output)
	}
	output, err = tool.Execute(context.Background(), json.RawMessage(`{
		"merge": false,
		"todos": [{"id":"inspect","status":"cancelled"}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "[cancelled] inspect: Inspect repository") || !strings.Contains(output, "build") {
		t.Fatalf("replace auto-upgrade did not preserve state: %s", output)
	}
}

func TestTodoWriteRejectsDuplicateIDsAsOutput(t *testing.T) {
	tool := &todoWriteTool{store: newTodoStore()}
	output, err := tool.Execute(context.Background(), json.RawMessage(`{
		"todos": [{"id":"same"},{"id":"same"}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "Duplicate todo ID") {
		t.Fatalf("unexpected duplicate result: %s", output)
	}
}
