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

func TestEditFileRequiresUniqueMatch(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "sample.txt")
	if err := os.WriteFile(path, []byte("old\nold\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	tool := editFileTool{ws: ws, approver: PromptApprover{Mode: PermissionAuto}}
	_, err = tool.Execute(context.Background(), json.RawMessage(`{"path":"sample.txt","old_text":"old","new_text":"new"}`))
	if err == nil || !strings.Contains(err.Error(), "occurs 2 times") {
		t.Fatalf("expected ambiguous edit error, got %v", err)
	}
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"sample.txt","old_text":"old","new_text":"new","replace_all":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "2 replacement") {
		t.Fatalf("unexpected result: %s", result)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new\nnew\n" {
		t.Fatalf("unexpected file content: %q", data)
	}
}

func TestRegistryNormalizesStringArguments(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry(ws, PromptApprover{Mode: PermissionDeny})
	output, err := registry.Execute(context.Background(), "read_file", json.RawMessage(`"{\"path\":\"a.txt\"}"`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "hello") {
		t.Fatalf("unexpected output: %s", output)
	}
}
