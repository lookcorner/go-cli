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
	if registry.HasTool("memory_search") || registry.HasTool("memory_get") {
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
	if err != nil || !strings.Contains(got, "**Lines:** 2 (from: 0, limit: 2)") || !strings.Contains(got, "1→remember release rollback steps") || !strings.HasSuffix(got, "2→") {
		t.Fatalf("get=%q err=%v", got, err)
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
	if !registry.HasTool("memory_search") || !registry.HasTool("memory_get") {
		t.Fatal("memory tools missing after enable")
	}
	if err := SetMemoryTools(registry, nil, cfg, false); err != nil {
		t.Fatal(err)
	}
	if registry.HasTool("memory_search") || registry.HasTool("memory_get") {
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
