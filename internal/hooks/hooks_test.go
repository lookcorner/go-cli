package hooks

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/lookcorner/go-cli/internal/api"
	"github.com/lookcorner/go-cli/internal/compat"
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

func TestPluginHooksFilterNonPluginEvents(t *testing.T) {
	inline := []byte(`{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"stop"}]}],"StopFailure":[{"hooks":[{"type":"command","command":"failure"}]}]}}`)
	catalog := DiscoverPlugins([]plugin.Plugin{{Name: "limited", Root: t.TempDir(), InlineHooks: inline, Executable: true}})
	snapshot := catalog.Snapshot()
	if len(snapshot.Hooks) != 1 || snapshot.Hooks[0].Event != Stop || len(snapshot.LoadErrors) != 1 {
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

func TestDiscoverGlobalAndTrustedProjectSources(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GROK_HOME", filepath.Join(home, ".grok"))
	root := t.TempDir()
	if err := exec.Command("git", "init", "-q", root).Run(); err != nil {
		t.Skipf("git unavailable: %v", err)
	}
	writeHookFixture(t, filepath.Join(home, ".grok", "hooks", "global.json"), "SessionStart", "global")
	writeHookFixture(t, filepath.Join(root, ".grok", "hooks", "project.json"), "PreToolUse", "project")
	writeHookFixture(t, filepath.Join(home, ".claude", "settings.json"), "SessionEnd", "claude")

	untrusted := Discover(Config{WorkspaceRoot: root, Compat: compat.Default()}).Snapshot()
	if names := hookNames(untrusted.Hooks); strings.Join(names, "|") != "global/global:session_start[0].hooks[0]|global/settings:session_end[0].hooks[0]" {
		t.Fatalf("untrusted hooks=%#v", names)
	}
	trusted := Discover(Config{WorkspaceRoot: root, Compat: compat.Default(), ProjectTrusted: true}).Snapshot()
	if names := hookNames(trusted.Hooks); !slices.Contains(names, "project/project:pre_tool_use[0].hooks[0]") {
		t.Fatalf("trusted hooks=%#v", names)
	}
	withoutClaude := compat.Default()
	withoutClaude.Claude.Hooks = false
	filtered := Discover(Config{WorkspaceRoot: root, Compat: withoutClaude}).Snapshot()
	if names := hookNames(filtered.Hooks); slices.Contains(names, "global/settings:session_end[0].hooks[0]") {
		t.Fatalf("Claude hook ignored compat gate: %#v", names)
	}
}

func TestCustomHookPathsAreConfinedAndReloadable(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	grokHome := filepath.Join(home, ".grok")
	t.Setenv("GROK_HOME", grokHome)
	custom := filepath.Join(grokHome, "team", "hooks.json")
	writeHookFixture(t, custom, "SessionStart", "custom")
	if err := AddPath(context.Background(), custom); err != nil {
		t.Fatal(err)
	}
	catalog := Discover(Config{WorkspaceRoot: t.TempDir(), Compat: compat.Default()})
	if names := hookNames(catalog.Snapshot().Hooks); !slices.Contains(names, "global/hooks:session_start[0].hooks[0]") {
		t.Fatalf("custom hooks=%#v", names)
	}
	if err := AddPath(context.Background(), filepath.Join(home, "outside.json")); err == nil {
		t.Fatal("custom hook outside GROK_HOME was accepted")
	}
	escape := t.TempDir()
	if err := os.Symlink(escape, filepath.Join(grokHome, "escape")); err == nil {
		if err := AddPath(context.Background(), filepath.Join(grokHome, "escape", "hooks.json")); err == nil {
			t.Fatal("symlink-escaping custom hook was accepted")
		}
	}
	if err := RemovePath(context.Background(), custom); err != nil {
		t.Fatal(err)
	}
	catalog.Reconfigure(Config{WorkspaceRoot: t.TempDir(), Compat: compat.Default()})
	if len(catalog.Snapshot().Hooks) != 0 {
		t.Fatalf("removed custom hook remained: %#v", catalog.Snapshot())
	}
}

func TestProjectHooksRequireGitWorktree(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GROK_HOME", filepath.Join(home, ".grok"))
	root := t.TempDir()
	writeHookFixture(t, filepath.Join(root, ".grok", "hooks", "project.json"), "PreToolUse", "project")
	snapshot := Discover(Config{WorkspaceRoot: root, Compat: compat.Default(), ProjectTrusted: true}).Snapshot()
	if len(snapshot.Hooks) != 0 {
		t.Fatalf("non-Git project hooks loaded: %#v", snapshot.Hooks)
	}
}

func writeHookFixture(t *testing.T, path, event, command string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	content := fmt.Sprintf(`{"hooks":{%q:[{"hooks":[{"type":"command","command":%q}]}]}}`, event, command)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func hookNames(specs []Spec) []string {
	names := make([]string, 0, len(specs))
	for _, spec := range specs {
		names = append(names, spec.Name)
	}
	sort.Strings(names)
	return names
}
