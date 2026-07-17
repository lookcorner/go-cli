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

func TestGorkFileToolCompatibility(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "sample.go"), []byte("one\nTwo\nthree\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry(ws, PromptApprover{Mode: PermissionAuto})
	defer registry.Close()
	read, err := registry.Execute(context.Background(), "read_file", json.RawMessage(
		`{"target_file":"sample.go","offset":-2,"limit":1}`,
	))
	if err != nil {
		t.Fatal(err)
	}
	if read != "3→three\n" {
		t.Fatalf("unexpected read output: %q", read)
	}
	listed, err := registry.Execute(context.Background(), "list_dir", json.RawMessage(
		`{"target_directory":"."}`,
	))
	if err != nil || !strings.Contains(listed, "sample.go") {
		t.Fatalf("unexpected list output=%q err=%v", listed, err)
	}
	found, err := registry.Execute(context.Background(), "grep", json.RawMessage(
		`{"pattern":"two","glob":"*.go","-i":true,"head_limit":10}`,
	))
	if err != nil || !strings.Contains(found, "sample.go:2:Two") {
		t.Fatalf("unexpected grep output=%q err=%v", found, err)
	}
	created, err := registry.Execute(context.Background(), "search_replace", json.RawMessage(
		`{"file_path":"created.txt","old_string":"","new_string":"old old\n"}`,
	))
	if err != nil || !strings.Contains(created, "created successfully") {
		t.Fatalf("unexpected create output=%q err=%v", created, err)
	}
	if _, err := registry.Execute(context.Background(), "search_replace", json.RawMessage(
		`{"file_path":"created.txt","old_string":"old","new_string":"new","replace_all":true}`,
	)); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "created.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new new\n" {
		t.Fatalf("unexpected edited content: %q", data)
	}
}
