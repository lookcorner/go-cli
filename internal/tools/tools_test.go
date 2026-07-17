package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lookcorner/go-cli/internal/api"
	"github.com/lookcorner/go-cli/internal/workspace"
)

type fixtureTool struct{ name string }

func (t fixtureTool) Definition() api.ToolDefinition {
	return api.ToolDefinition{Type: "function", Name: t.name, Parameters: map[string]any{"type": "object"}}
}
func (t fixtureTool) Execute(context.Context, json.RawMessage) (string, error) { return t.name, nil }

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

func TestEditFileUnicodeTypographyFallback(t *testing.T) {
	for name, test := range map[string]struct {
		content string
		oldText string
		newText string
		want    string
	}{
		"smart quotes": {"say \u201chello\u201d\n", `"hello"`, `"goodbye"`, "say \"goodbye\"\n"},
		"em dash":      {"foo\u2014bar\n", "foo--bar", "foo-bar", "foo-bar\n"},
		"nbsp":         {"hello\u00a0world\n", "hello world", "hello_world", "hello_world\n"},
		"ellipsis":     {"wait\u2026\n", "wait...", "done", "done\n"},
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "sample.txt")
			if err := os.WriteFile(path, []byte(test.content), 0o600); err != nil {
				t.Fatal(err)
			}
			ws, err := workspace.Open(root)
			if err != nil {
				t.Fatal(err)
			}
			tool := editFileTool{ws: ws, approver: PromptApprover{Mode: PermissionAuto}}
			raw, _ := json.Marshal(map[string]any{"path": "sample.txt", "old_text": test.oldText, "new_text": test.newText})
			result, err := tool.Execute(context.Background(), raw)
			if err != nil || !strings.Contains(result, "Unicode normalization") {
				t.Fatalf("normalized edit result=%q err=%v", result, err)
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != test.want {
				t.Fatalf("content=%q want=%q err=%v", data, test.want, err)
			}
		})
	}
}

func TestEditFilePreservesCRLF(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "sample.txt")
	if err := os.WriteFile(path, []byte("first\r\nsecond\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	tool := editFileTool{ws: ws, approver: PromptApprover{Mode: PermissionAuto}}
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"sample.txt","old_text":"first\nsecond","new_text":"updated\nlines"}`)); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "updated\r\nlines\r\n" {
		t.Fatalf("CRLF was not preserved: %q err=%v", data, err)
	}
}

func TestEditFileRejectsPartialUnicodeExpansion(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "sample.txt")
	if err := os.WriteFile(path, []byte("before\u2014after\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	tool := editFileTool{ws: ws, approver: PromptApprover{Mode: PermissionAuto}}
	_, err = tool.Execute(context.Background(), json.RawMessage(`{"path":"sample.txt","old_text":"-","new_text":"x"}`))
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("partial em-dash match should fail closed: %v", err)
	}
}

func TestRegistryReplaceAtomicallyUpdatesDefinitions(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry(ws, PromptApprover{Mode: PermissionAuto})
	defer registry.Close()
	if err := registry.Register(fixtureTool{name: "dynamic_old"}); err != nil {
		t.Fatal(err)
	}
	names, err := registry.Replace([]string{"dynamic_old"}, []Tool{fixtureTool{name: "dynamic_new"}})
	if err != nil || len(names) != 1 || names[0] != "dynamic_new" {
		t.Fatalf("unexpected replacement: %#v err=%v", names, err)
	}
	if _, err := registry.Execute(context.Background(), "dynamic_old", json.RawMessage(`{}`)); err == nil {
		t.Fatal("old tool remained registered")
	}
	if output, err := registry.Execute(context.Background(), "dynamic_new", json.RawMessage(`{}`)); err != nil || output != "dynamic_new" {
		t.Fatalf("new tool unavailable: %q err=%v", output, err)
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

func TestListDirTreeRespectsGitIgnoreAndHiddenFiles(t *testing.T) {
	root := t.TempDir()
	command := exec.Command("git", "init", "-q")
	command.Dir = root
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	globalIgnore := filepath.Join(t.TempDir(), "global-ignore")
	if err := os.WriteFile(globalIgnore, []byte("global.tmp\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	command = exec.Command("git", "config", "core.excludesFile", globalIgnore)
	command.Dir = root
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git config: %v: %s", err, output)
	}
	files := map[string]string{
		"visible.txt":           "visible",
		"ignored.log":           "ignored",
		"global.tmp":            "ignored globally",
		".hidden":               "hidden",
		"nested/keep.txt":       "keep",
		"nested/private.secret": "secret",
		"nested/.gitignore":     "*.secret\n",
		".gitignore":            "*.log\nignored-dir/\n",
		"ignored-dir/file.txt":  "ignored directory",
	}
	for name, content := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	output, err := (&listDirTool{ws: ws}).Execute(context.Background(), json.RawMessage(`{"target_directory":"."}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"- visible.txt", "- nested/", "- keep.txt"} {
		if !strings.Contains(output, want) {
			t.Fatalf("missing %q from tree:\n%s", want, output)
		}
	}
	for _, absent := range []string{"ignored.log", "global.tmp", ".hidden", "private.secret", "ignored-dir"} {
		if strings.Contains(output, absent) {
			t.Fatalf("ignored entry %q appeared:\n%s", absent, output)
		}
	}
}

func TestListDirTreeUsesOutputBudget(t *testing.T) {
	root := t.TempDir()
	for index := range 150 {
		name := fmt.Sprintf("%03d-%s.txt", index, strings.Repeat("x", 80))
		if err := os.WriteFile(filepath.Join(root, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	output, err := (&listDirTool{ws: ws}).Execute(context.Background(), json.RawMessage(`{"target_directory":"."}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "too large to list fully") || strings.Contains(output, "149-") {
		t.Fatalf("directory output was not budgeted: len=%d tail=%q", len(output), output[max(0, len(output)-200):])
	}
}

func TestListDirSummarizesLargeSubdirectory(t *testing.T) {
	root := t.TempDir()
	command := exec.Command("git", "init", "-q")
	command.Dir = root
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("*.skip\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for extension, count := range map[string]int{"go": 70, "txt": 40, "": 20, "md": 10, "skip": 30} {
		for index := range count {
			name := fmt.Sprintf("%03d-%s", index, strings.Repeat(extension+"x", 35))
			if extension != "" {
				name += "." + extension
			}
			path := filepath.Join(root, "many", name)
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := os.WriteFile(filepath.Join(root, "sibling.txt"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	output, err := (&listDirTool{ws: ws}).Execute(context.Background(), json.RawMessage(`{"target_directory":"."}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"- many/", "[140 files in subtree: 70 *.go, 40 *.txt, 20 *no-ext, ...]", "- sibling.txt"} {
		if !strings.Contains(output, want) {
			t.Fatalf("missing %q from summarized tree:\n%s", want, output)
		}
	}
	if strings.Contains(output, "000-") || strings.Contains(output, "too large to list fully") {
		t.Fatalf("collapsed subtree should replace individual entries without truncating:\n%s", output)
	}
}
