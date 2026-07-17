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
