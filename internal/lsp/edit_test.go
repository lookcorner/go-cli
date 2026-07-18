package lsp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/lookcorner/go-cli/internal/workspace"
)

func TestWorkspaceEditPreflightRejectsStaleAndEscapingChanges(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "main.go")
	if err := os.WriteFile(path, []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ws, _ := workspace.Open(root)
	resolved, _ := ws.Resolve(path)
	uri := fileURI(resolved)
	client := &Client{root: ws.Root(), workspace: ws, documents: map[string]documentState{
		uri: {content: "package main\n", version: 2},
	}}
	stale := json.RawMessage(`{"edit":{"documentChanges":[{"textDocument":{"uri":"` + uri + `","version":1},"edits":[]}]}}`)
	if result := client.applyWorkspaceEdit(stale); result["applied"] != false {
		t.Fatalf("stale version was applied: %#v", result)
	}
	resource := json.RawMessage(`{"edit":{"documentChanges":[{"kind":"create","uri":"` + uri + `"}]}}`)
	if result := client.applyWorkspaceEdit(resource); result["applied"] != false {
		t.Fatalf("resource operation was applied: %#v", result)
	}
	escapeURI := fileURI(filepath.Join(t.TempDir(), "outside.go"))
	escape := json.RawMessage(`{"edit":{"changes":{"` + uri + `":[],"` + escapeURI + `":[]}}}`)
	if result := client.applyWorkspaceEdit(escape); result["applied"] != false {
		t.Fatalf("escaping edit was applied: %#v", result)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "package main\n" {
		t.Fatalf("failed preflight changed file: %q", data)
	}
}

func TestWorkspaceEditAppliesFileResourceOperationsInOrder(t *testing.T) {
	root := t.TempDir()
	original := filepath.Join(root, "main.go")
	if err := os.WriteFile(original, []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ws, _ := workspace.Open(root)
	createdURI := fileURI(filepath.Join(root, "created.go"))
	renamed := filepath.Join(root, "renamed.go")
	renamedURI := fileURI(renamed)
	originalURI := fileURI(original)
	client := &Client{root: ws.Root(), workspace: ws, documents: make(map[string]documentState), diagnostics: make(map[string]json.RawMessage)}
	request := json.RawMessage(`{"edit":{"documentChanges":[` +
		`{"kind":"create","uri":"` + createdURI + `"},` +
		`{"textDocument":{"uri":"` + createdURI + `"},"edits":[{"range":{"start":{"line":0,"character":0},"end":{"line":0,"character":0}},"newText":"package generated\n"}]},` +
		`{"kind":"rename","oldUri":"` + createdURI + `","newUri":"` + renamedURI + `"},` +
		`{"kind":"delete","uri":"` + originalURI + `"}` +
		`]}}`)
	result := client.applyWorkspaceEdit(request)
	if result["applied"] != true {
		t.Fatalf("resource edit failed: %#v", result)
	}
	data, err := os.ReadFile(renamed)
	if err != nil || string(data) != "package generated\n" {
		t.Fatalf("renamed content=%q err=%v", data, err)
	}
	if _, err := os.Stat(filepath.Join(root, "created.go")); !os.IsNotExist(err) {
		t.Fatalf("rename source remained: %v", err)
	}
	if _, err := os.Stat(original); !os.IsNotExist(err) {
		t.Fatalf("deleted file remained: %v", err)
	}
}
