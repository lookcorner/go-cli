package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lookcorner/go-cli/internal/workspace"
)

func TestPathNotFoundHintsAcrossFileTools(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "helpers.go"), []byte("package helpers\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "helper_test.go"), []byte("package helpers\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry(ws, PromptApprover{Mode: PermissionAuto})
	defer registry.Close()
	registry.SetPathNotFoundHints(true)

	for _, test := range []struct {
		name string
		tool string
		args map[string]any
	}{
		{name: "read", tool: "read_file", args: map[string]any{"target_file": "helper.go"}},
		{name: "list", tool: "list_dir", args: map[string]any{"target_directory": "helper"}},
		{name: "grep", tool: "grep", args: map[string]any{"pattern": "package", "path": "helper"}},
		{name: "replace", tool: "search_replace", args: map[string]any{"file_path": "helper.go", "old_string": "package", "new_string": "module"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw, _ := json.Marshal(test.args)
			_, err := registry.Execute(context.Background(), test.tool, raw)
			if !errors.Is(err, os.ErrNotExist) || !strings.Contains(err.Error(), "Similar entries in parent directory:") ||
				!strings.Contains(err.Error(), "helpers.go") || !strings.Contains(err.Error(), "Note: your current working directory is "+filepath.ToSlash(ws.Root())) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestPathNotFoundHintsSuggestDroppedWorkspaceDirectory(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "repo")
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o700); err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry(ws, PromptApprover{Mode: PermissionAuto})
	defer registry.Close()
	registry.SetPathNotFoundHints(true)

	requested := filepath.Join(filepath.Dir(ws.Root()), "src")
	if got := suggestUnderWorkspace(requested, ws.Root()); got != filepath.Join(ws.Root(), "src") {
		t.Fatalf("suggestion=%q requested=%q root=%q", got, requested, ws.Root())
	}
	if _, err := resolveToolPath(ws, requested, true); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("resolve hint=%v", err)
	}
	readTool := registry.tools["read_file"].(*readFileTool)
	if readTool.pathHints == nil || !readTool.pathHints.Load() {
		t.Fatal("read tool did not receive path hint state")
	}
	_, err = registry.Execute(context.Background(), "read_file", json.RawMessage(`{"target_file":`+quoted(requested)+`}`))
	if !errors.Is(err, os.ErrNotExist) || !strings.Contains(err.Error(), "Did you mean "+filepath.ToSlash(filepath.Join(ws.Root(), "src"))+"?") {
		t.Fatalf("err=%v", err)
	}
}

func TestPathNotFoundHintsDisabledPreservesErrors(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry(ws, PromptApprover{Mode: PermissionAuto})
	defer registry.Close()
	_, err = registry.Execute(context.Background(), "read_file", json.RawMessage(`{"target_file":"missing.txt"}`))
	if err == nil || strings.Contains(err.Error(), "current working directory") || !strings.Contains(err.Error(), `open "missing.txt"`) {
		t.Fatalf("err=%v", err)
	}
}

func TestPathNotFoundHintsCoverMissingParentsWithoutWeakeningConfinement(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry(ws, PromptApprover{Mode: PermissionAuto})
	defer registry.Close()
	registry.SetPathNotFoundHints(true)

	_, err = registry.Execute(context.Background(), "read_file", json.RawMessage(`{"target_file":"missing/child.txt"}`))
	if !errors.Is(err, os.ErrNotExist) || !strings.Contains(err.Error(), "current working directory") {
		t.Fatalf("missing parent err=%v", err)
	}
	_, err = registry.Execute(context.Background(), "read_file", json.RawMessage(`{"target_file":"../outside.txt"}`))
	if err == nil || errors.Is(err, os.ErrNotExist) || !strings.Contains(err.Error(), "escapes workspace") ||
		strings.Contains(err.Error(), "current working directory") {
		t.Fatalf("escape err=%v", err)
	}
}

func TestReadFileMediaPathsUsePathNotFoundHints(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry(ws, PromptApprover{Mode: PermissionAuto})
	defer registry.Close()
	registry.SetPathNotFoundHints(true)
	for _, path := range []string{"missing.png", "missing.pdf"} {
		_, err := registry.ExecuteResult(context.Background(), "read_file", json.RawMessage(`{"target_file":`+quoted(path)+`}`))
		if !errors.Is(err, os.ErrNotExist) || !strings.Contains(err.Error(), "Note: your current working directory is") {
			t.Fatalf("path=%q err=%v", path, err)
		}
	}
}

func TestHashlinePathHintsAndWriteCreation(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "helpers.go"), []byte("package helpers\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry(ws, PromptApprover{Mode: PermissionAuto})
	defer registry.Close()
	if err := registry.ConfigureFileToolset("hashline", "chunk", 3, 8); err != nil {
		t.Fatal(err)
	}
	registry.SetPathNotFoundHints(true)

	for _, test := range []struct {
		tool string
		raw  string
	}{
		{tool: "hashline_read", raw: `{"target_file":"helper.go"}`},
		{tool: "hashline_grep", raw: `{"pattern":"package","path":"helper.go"}`},
		{tool: "hashline_edit", raw: `{"file_path":"helper.go","edits":[{"op":"replace","anchor":"1:abc","content":"package changed"}]}`},
	} {
		_, err := registry.Execute(context.Background(), test.tool, json.RawMessage(test.raw))
		if !errors.Is(err, os.ErrNotExist) || !strings.Contains(err.Error(), "helpers.go") {
			t.Fatalf("%s err=%v", test.tool, err)
		}
	}

	_, err = registry.Execute(context.Background(), "hashline_edit", json.RawMessage(
		`{"file_path":"created.go","edits":[{"op":"write","content":"package created\n"}]}`,
	))
	if err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(root, "created.go")); err != nil || string(data) != "package created\n" {
		t.Fatalf("data=%q err=%v", data, err)
	}
}

func TestPathNotFoundHintsFollowWorkspaceRegistry(t *testing.T) {
	first, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	secondRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(secondRoot, "target.txt"), []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := workspace.Open(secondRoot)
	if err != nil {
		t.Fatal(err)
	}
	parent := NewRegistry(first, PromptApprover{Mode: PermissionAuto})
	defer parent.Close()
	parent.SetPathNotFoundHints(true)
	child := parent.ForWorkspace(second)
	defer child.Close()

	_, err = child.Execute(context.Background(), "read_file", json.RawMessage(`{"target_file":"targe.txt"}`))
	if !errors.Is(err, os.ErrNotExist) || !strings.Contains(err.Error(), "target.txt") {
		t.Fatalf("err=%v", err)
	}
}
