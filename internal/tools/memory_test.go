package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lookcorner/go-cli/internal/memory"
	"github.com/lookcorner/go-cli/internal/workspace"
)

func TestMemoryToolsRegisterOnlyWhenEnabledAndFormatResults(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store, err := memory.Open(t.TempDir(), ws.Root(), "tools")
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry(ws, PromptApprover{Mode: PermissionAuto})
	defer registry.Close()
	cfg := memory.DefaultConfig()
	if err := RegisterMemoryTools(registry, store, cfg); err != nil {
		t.Fatal(err)
	}
	if registry.HasTool("memory_search") || registry.HasTool("memory_get") || registry.HasTool("memory_edit") {
		t.Fatal("disabled memory tools were registered")
	}
	cfg.Enabled = true
	if err := RegisterMemoryTools(registry, store, cfg); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(storeRootFromList(t, store), "MEMORY.md"), []byte("remember release rollback steps\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	search, err := registry.Execute(context.Background(), "memory_search", json.RawMessage(`{"query":"release rollback"}`))
	if err != nil || !strings.Contains(search, "Found 1 memory result(s)") || !strings.Contains(search, "score: 1.00, source: global") {
		t.Fatalf("search=%q err=%v", search, err)
	}
	path := strings.Split(strings.Split(search, "**File:** ")[1], " (lines")[0]
	got, err := registry.Execute(context.Background(), "memory_get", json.RawMessage(`{"path":`+quoted(path)+`,"from":0,"lines":2}`))
	if err != nil || !strings.Contains(got, "**Lines:** 1 (from: 0, limit: 2)") || !strings.HasSuffix(got, "1→remember release rollback steps") {
		t.Fatalf("get=%q err=%v", got, err)
	}
	got, err = registry.Execute(context.Background(), "memory_get", json.RawMessage(`{"path":`+quoted(path)+`}`))
	if err != nil || !strings.Contains(got, "**Lines:** 1 (from: start, limit: all)") || !strings.HasSuffix(got, "2→") {
		t.Fatalf("full get=%q err=%v", got, err)
	}
	got, err = registry.Execute(context.Background(), "memory_get", json.RawMessage(`{"path":`+quoted(path)+`,"lines":0}`))
	if err != nil || !strings.Contains(got, "**Lines:** 0 (from: start, limit: 0)") || strings.Contains(got, "1→") {
		t.Fatalf("zero get=%q err=%v", got, err)
	}
	if noMatch, err := registry.Execute(context.Background(), "memory_search", json.RawMessage(`{"query":"absent"}`)); err != nil || noMatch != "No memory results found for query." {
		t.Fatalf("noMatch=%q err=%v", noMatch, err)
	}
}

func TestMemoryToolsToggleAtomicallyAndParseCommands(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store, err := memory.Open(t.TempDir(), ws.Root(), "toggle")
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry(ws, PromptApprover{Mode: PermissionAuto})
	defer registry.Close()
	cfg := memory.DefaultConfig()
	cfg.Enabled = true
	if err := SetMemoryTools(registry, store, cfg, true); err != nil {
		t.Fatal(err)
	}
	if !registry.HasTool("memory_search") || !registry.HasTool("memory_get") || !registry.HasTool("memory_edit") {
		t.Fatal("memory tools missing after enable")
	}
	if err := SetMemoryTools(registry, nil, cfg, false); err != nil {
		t.Fatal(err)
	}
	if registry.HasTool("memory_search") || registry.HasTool("memory_get") || registry.HasTool("memory_edit") {
		t.Fatal("memory tools survived disable")
	}
	for input, want := range map[string]string{"/memory": "browse", "/mem status": "browse", "/memory ON": "enable", "/mem disable": "disable"} {
		if got, ok := ParseMemoryCommand(input); !ok || got != want {
			t.Fatalf("ParseMemoryCommand(%q)=%q,%v", input, got, ok)
		}
	}
	if _, ok := ParseMemoryCommand("remember memory"); ok {
		t.Fatal("non-command parsed as memory command")
	}
	for input, want := range map[string]string{"/remember": "", "/remember deploy through eu-west": "deploy through eu-west"} {
		if got, ok := ParseRememberCommand(input); !ok || got != want {
			t.Fatalf("ParseRememberCommand(%q)=%q,%v", input, got, ok)
		}
	}
	if _, ok := ParseRememberCommand("/remembered value"); ok {
		t.Fatal("remember prefix collision")
	}
}

func storeRootFromList(t *testing.T, store *memory.Store) string {
	t.Helper()
	path, _, err := store.Write("probe", "temporary probe")
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Dir(filepath.Dir(filepath.Dir(path)))
}

func quoted(value string) string { data, _ := json.Marshal(value); return string(data) }

func TestMemoryEditToolRequiresApprovalAndEdits(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store, err := memory.Open(t.TempDir(), ws.Root(), "edit")
	if err != nil {
		t.Fatal(err)
	}
	global := filepath.Join(storeRootFromList(t, store), "MEMORY.md")
	if err := os.WriteFile(global, []byte("one\ntwo\nthree\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := memory.DefaultConfig()
	cfg.Enabled = true

	denied := NewRegistry(ws, PromptApprover{Mode: PermissionDeny})
	defer denied.Close()
	if err := SetMemoryTools(denied, store, cfg, true); err != nil {
		t.Fatal(err)
	}
	if _, err := denied.Execute(context.Background(), "memory_edit", json.RawMessage(`{"path":`+quoted(global)+`,"from":1,"lines":1,"new_text":"2nd"}`)); !IsPermissionDenied(err) {
		t.Fatalf("denied err=%v", err)
	}

	registry := NewRegistry(ws, PromptApprover{Mode: PermissionAlwaysApprove})
	defer registry.Close()
	if err := SetMemoryTools(registry, store, cfg, true); err != nil {
		t.Fatal(err)
	}
	out, err := registry.Execute(context.Background(), "memory_edit", json.RawMessage(`{"path":`+quoted(global)+`,"from":1,"lines":1,"new_text":"2nd"}`))
	if err != nil || !strings.Contains(out, "replaced 1 line(s) from line 1") {
		t.Fatalf("edit=%q err=%v", out, err)
	}
	if data, _ := os.ReadFile(global); string(data) != "one\n2nd\nthree\n" {
		t.Fatalf("content=%q", data)
	}
	out, err = registry.Execute(context.Background(), "memory_edit", json.RawMessage(`{"path":`+quoted(global)+`,"from":1,"lines":1,"new_text":""}`))
	if err != nil || !strings.Contains(out, "Edited") {
		t.Fatalf("forget=%q err=%v", out, err)
	}
	if data, _ := os.ReadFile(global); string(data) != "one\nthree\n" {
		t.Fatalf("forget content=%q", data)
	}
	if out, err := registry.Execute(context.Background(), "memory_edit", json.RawMessage(`{"path":`+quoted(global)+`,"new_text":"one\nthree"}`)); err != nil || !strings.HasPrefix(out, "No changes") {
		t.Fatalf("no-op=%q err=%v", out, err)
	}
}
