package hooks

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/lookcorner/go-cli/internal/api"
	"github.com/lookcorner/go-cli/internal/plugin"
)

func TestPluginHookCanDenyToolAndReceivesAuthenticEnvironment(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	root := t.TempDir()
	hooksDir := filepath.Join(root, "hooks")
	if err := os.MkdirAll(hooksDir, 0o700); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(hooksDir, "deny.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ncat >/dev/null\nprintf '{\"decision\":\"deny\",\"reason\":\"%s:%s\"}' \"$GROK_HOOK_EVENT\" \"$GROK_PLUGIN_ROOT\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	config := `{"hooks":{"PreToolUse":[{"matcher":"shell|write_file","hooks":[{"type":"command","command":"./deny.sh","env":{"GROK_HOOK_EVENT":"spoofed"}}]}]}}`
	if err := os.WriteFile(filepath.Join(hooksDir, "hooks.json"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	item := plugin.Plugin{Name: "guard", Root: root, DataDir: filepath.Join(home, "data"), HooksConfig: filepath.Join(hooksDir, "hooks.json"), Executable: true}
	catalog := DiscoverPlugins([]plugin.Plugin{item})
	runner := &Runtime{Catalog: catalog, WorkspaceRoot: root, SessionID: "session-1"}
	err := runner.BeforeTool(context.Background(), api.ToolCall{CallID: "call-1", Name: "shell", Arguments: []byte(`{"command":"true"}`)})
	var denied *DeniedError
	if !errors.As(err, &denied) || denied.Hook == "" || denied.Reason != "pre_tool_use:"+root {
		t.Fatalf("denial=%#v err=%v", denied, err)
	}
	if err := runner.BeforeTool(context.Background(), api.ToolCall{Name: "read_file", Arguments: []byte(`{}`)}); err != nil {
		t.Fatalf("matcher blocked unrelated tool: %v", err)
	}
}

func TestHookFailuresFailOpenAndDisabledStatePersists(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	root := t.TempDir()
	hooksDir := filepath.Join(root, "hooks")
	if err := os.MkdirAll(hooksDir, 0o700); err != nil {
		t.Fatal(err)
	}
	config := `{"hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"./missing"}]}]}}`
	if err := os.WriteFile(filepath.Join(hooksDir, "hooks.json"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	item := plugin.Plugin{Name: "guard", Root: root, HooksConfig: filepath.Join(hooksDir, "hooks.json"), Executable: true}
	catalog := DiscoverPlugins([]plugin.Plugin{item})
	runner := &Runtime{Catalog: catalog, WorkspaceRoot: root}
	if err := runner.BeforeTool(context.Background(), api.ToolCall{Name: "shell", Arguments: []byte(`{}`)}); err != nil {
		t.Fatalf("hook failure did not fail open: %v", err)
	}
	snapshot := catalog.Snapshot()
	if len(snapshot.Hooks) != 1 {
		t.Fatalf("snapshot=%#v", snapshot)
	}
	name := snapshot.Hooks[0].Name
	if err := catalog.SetDisabled(context.Background(), []string{name}, true); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(home, "disabled-hooks"))
	if err != nil || strings.TrimSpace(string(data)) != name || !catalog.Snapshot().Hooks[0].Disabled {
		t.Fatalf("disabled file=%q snapshot=%#v err=%v", data, catalog.Snapshot(), err)
	}
}

func TestInvalidHookEntriesAreReportedWithoutDiscardingValidHooks(t *testing.T) {
	root := t.TempDir()
	hooksDir := filepath.Join(root, "hooks")
	if err := os.MkdirAll(hooksDir, 0o700); err != nil {
		t.Fatal(err)
	}
	config := `{"hooks":{"Unknown":[{"hooks":[]}],"PreToolUse":[{"matcher":"[","hooks":[{"type":"command","command":"x"}]},{"hooks":[{"type":"command","command":"x"}]}]}}`
	path := filepath.Join(hooksDir, "hooks.json")
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog := DiscoverPlugins([]plugin.Plugin{{Name: "mixed", Root: root, HooksConfig: path, Executable: true}})
	snapshot := catalog.Snapshot()
	if len(snapshot.Hooks) != 1 || len(snapshot.LoadErrors) != 2 {
		t.Fatalf("snapshot=%#v", snapshot)
	}
}

func TestInlinePluginHooks(t *testing.T) {
	root := t.TempDir()
	inline := []byte(`{"hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"check"}]}]}}`)
	catalog := DiscoverPlugins([]plugin.Plugin{{Name: "inline", Root: root, InlineHooks: inline, Executable: true}})
	snapshot := catalog.Snapshot()
	if len(snapshot.Hooks) != 1 || snapshot.Hooks[0].SourceDir != root || !strings.Contains(snapshot.Hooks[0].Name, "plugin/inline/plugin:") {
		t.Fatalf("snapshot=%#v", snapshot)
	}
}

func TestDisabledChangesMergeAcrossCatalogs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	first := DiscoverPlugins(nil)
	second := DiscoverPlugins(nil)
	if err := first.SetDisabled(context.Background(), []string{"one"}, true); err != nil {
		t.Fatal(err)
	}
	if err := second.SetDisabled(context.Background(), []string{"two"}, true); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(home, "disabled-hooks"))
	if err != nil || strings.TrimSpace(string(data)) != "one\ntwo" {
		t.Fatalf("disabled=%q err=%v", data, err)
	}
}

func TestHTTPHookRejectsUnsafeTargets(t *testing.T) {
	for _, target := range []string{"http://example.com/hook", "https://10.0.0.1/hook", "https://169.254.169.254/latest"} {
		if _, _, err := runHTTP(context.Background(), Spec{URL: target}, []byte(`{}`)); err == nil {
			t.Fatalf("unsafe target accepted: %s", target)
		}
	}
}
