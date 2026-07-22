package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/lookcorner/go-cli/internal/api"
	"github.com/lookcorner/go-cli/internal/compat"
	"github.com/lookcorner/go-cli/internal/plugin"
	"github.com/lookcorner/go-cli/internal/tools"
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

func TestPermissionDeniedHookReceivesToolPayload(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	root := t.TempDir()
	payloadPath := filepath.Join(root, "payload.json")
	script := filepath.Join(root, "capture.sh")
	if err := os.WriteFile(script, []byte(fmt.Sprintf("#!/bin/sh\ncat > %q\n", payloadPath)), 0o700); err != nil {
		t.Fatal(err)
	}
	catalog := &Catalog{specs: []Spec{{Event: PermissionDenied, Command: script, Timeout: time.Second}}}
	runner := &Runtime{Catalog: catalog, WorkspaceRoot: root, SessionID: "session-1"}
	call := api.ToolCall{CallID: "call-1", Name: "shell", Arguments: []byte(`{"command":"git push"}`)}
	runner.AfterTool(context.Background(), call, tools.ExecutionResult{}, &tools.PermissionDeniedError{Action: "shell"})
	payload, err := os.ReadFile(payloadPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(payload)
	for _, expected := range []string{`"hookEventName":"permission_denied"`, `"toolName":"shell"`, `"toolUseId":"call-1"`, `"command":"git push"`} {
		if !strings.Contains(text, expected) {
			t.Fatalf("payload=%s missing %s", text, expected)
		}
	}
}

func TestNotificationHookMatchesTaskCompletion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	root := t.TempDir()
	payloadPath := filepath.Join(root, "notification.json")
	script := filepath.Join(root, "capture.sh")
	if err := os.WriteFile(script, []byte(fmt.Sprintf("#!/bin/sh\ncat > %q\n", payloadPath)), 0o700); err != nil {
		t.Fatal(err)
	}
	catalog := &Catalog{specs: []Spec{{
		Event: Notification, Command: script, Matcher: "^task_complete$", matcher: regexp.MustCompile("^task_complete$"), Timeout: time.Second,
	}}}
	runner := &Runtime{Catalog: catalog, WorkspaceRoot: root, SessionID: "session-1"}
	runner.Notification(context.Background(), "task_complete", "Background task completed: task-1", "", "info")
	payload, err := os.ReadFile(payloadPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(payload)
	for _, expected := range []string{`"hookEventName":"notification"`, `"notificationType":"task_complete"`, `"message":"Background task completed: task-1"`, `"level":"info"`} {
		if !strings.Contains(text, expected) {
			t.Fatalf("payload=%s missing %s", text, expected)
		}
	}
}

func TestAgentErrorAndIdleNotificationHooks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	t.Setenv("GROK_IDLE_NOTIFICATION_DELAY_MS", "10")
	root := t.TempDir()
	payloadPath := filepath.Join(root, "notifications.jsonl")
	script := filepath.Join(root, "capture.sh")
	if err := os.WriteFile(script, []byte(fmt.Sprintf("#!/bin/sh\ncat >> %q\nprintf '\\n' >> %q\n", payloadPath, payloadPath)), 0o700); err != nil {
		t.Fatal(err)
	}
	catalog := &Catalog{specs: []Spec{{Event: Notification, Command: script, Timeout: time.Second}}}
	runner := &Runtime{Catalog: catalog, WorkspaceRoot: root, SessionID: "session-1"}
	runner.Stopped(context.Background(), "failed", errors.New("model unavailable"))
	runner.Stopped(context.Background(), "completed", nil)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		data, _ := os.ReadFile(payloadPath)
		if strings.Contains(string(data), `"notificationType":"idle_prompt"`) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	data, err := os.ReadFile(payloadPath)
	if err != nil || !strings.Contains(string(data), `"notificationType":"agent_error"`) || !strings.Contains(string(data), `"message":"model unavailable"`) || !strings.Contains(string(data), `"level":"error"`) || !strings.Contains(string(data), `"notificationType":"idle_prompt"`) || !strings.Contains(string(data), `"message":"Turn complete"`) {
		t.Fatalf("notifications=%q err=%v", data, err)
	}
	if err := os.WriteFile(payloadPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	runner.Stopped(context.Background(), "completed", nil)
	runner.UserPromptSubmitted(context.Background(), "next")
	time.Sleep(30 * time.Millisecond)
	if data, _ := os.ReadFile(payloadPath); strings.TrimSpace(string(data)) != "" {
		t.Fatalf("cancelled idle notification fired: %s", data)
	}
	runner.Stopped(context.Background(), "completed", nil)
	runner.SessionEnded(context.Background(), "closed")
	time.Sleep(30 * time.Millisecond)
	if data, _ := os.ReadFile(payloadPath); strings.TrimSpace(string(data)) != "" {
		t.Fatalf("closed runtime emitted idle notification: %s", data)
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

func TestCatalogWithInlineHooksIsIsolatedAndRemapsStop(t *testing.T) {
	root := t.TempDir()
	base := &Catalog{specs: []Spec{{Name: "global", Event: PreToolUse}}}
	inline := []byte(`{"Stop":[{"hooks":[{"type":"command","command":"./done.sh"}]}],"PostToolUse":[{"hooks":[{"type":"command","command":"./after.sh"}]}]}`)
	child := base.WithInline(inline, root, "agent/reviewer/", "agent reviewer")
	if len(base.Snapshot().Hooks) != 1 {
		t.Fatal("inline hooks mutated parent catalog")
	}
	snapshot := child.Snapshot()
	var stop, post bool
	for _, spec := range snapshot.Hooks {
		if spec.Event == SubagentStop && spec.SourceDir == root && strings.HasPrefix(spec.Name, "agent/reviewer/") {
			stop = true
		}
		if spec.Event == PostToolUse && spec.SourceDir == root {
			post = true
		}
	}
	if len(snapshot.Hooks) != 3 || !stop || !post {
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

func TestClientHooksGateObserveTimeoutAndInherit(t *testing.T) {
	pre, ok := NewClientHookGroup("PreToolUse", "shell|write_file", []string{"allow", "deny"}, time.Second)
	if !ok {
		t.Fatal("valid pre-tool client hook rejected")
	}
	post, ok := NewClientHookGroup("post_tool_use", "*", []string{"observe"}, 0)
	if !ok {
		t.Fatal("valid post-tool client hook rejected")
	}
	notification, ok := NewClientHookGroup("Notification", "does-not-match", []string{"lifecycle"}, 0)
	if !ok {
		t.Fatal("valid notification client hook rejected")
	}
	type dispatch struct {
		id       string
		blocking bool
		envelope map[string]any
	}
	calls := make(chan dispatch, 8)
	catalog := AttachClient(nil, []ClientHookGroup{pre, post, notification}, func(_ context.Context, id string, envelope map[string]any, blocking bool) (string, string) {
		calls <- dispatch{id: id, blocking: blocking, envelope: envelope}
		if id == "deny" {
			return "deny", "policy rejected write"
		}
		return "continue", ""
	})
	runtime := &Runtime{Catalog: catalog, WorkspaceRoot: "/work", SessionID: "parent", TranscriptPath: "/sessions/parent.jsonl"}
	ctx := WithPromptID(context.Background(), "prompt-1")
	err := runtime.BeforeTool(ctx, api.ToolCall{CallID: "call-1", Name: "write_file", Arguments: json.RawMessage(`{"path":"a.go"}`)})
	var denied *DeniedError
	if !errors.As(err, &denied) || denied.Hook != "client:deny" || denied.Reason != "policy rejected write" {
		t.Fatalf("denial=%#v err=%v", denied, err)
	}
	runtime.AfterTool(ctx, api.ToolCall{CallID: "call-1", Name: "write_file", Arguments: json.RawMessage(`{}`)}, tools.ExecutionResult{Output: "done"}, nil)

	seenObserve := false
	for range 3 {
		call := <-calls
		if call.id == "observe" {
			seenObserve = true
			if call.blocking || call.envelope["sessionId"] != "parent" || call.envelope["promptId"] != "prompt-1" || call.envelope["transcriptPath"] != "/sessions/parent.jsonl" || call.envelope["toolResult"] != "done" {
				t.Fatalf("observe dispatch=%#v", call)
			}
		}
	}
	if !seenObserve {
		t.Fatal("post-tool observer was not called")
	}
	runtime.Notification(context.Background(), "idle_prompt", "done", "", "info")
	if call := <-calls; call.id != "lifecycle" || call.envelope["notificationType"] != "idle_prompt" {
		t.Fatalf("lifecycle matcher dispatch=%#v", call)
	}

	inherited := catalog.WithInline([]byte(`{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"noop"}]}]}}`), "/work", "agent/test/", "agent test")
	child := &Runtime{Catalog: inherited, WorkspaceRoot: "/work", SessionID: "child"}
	child.AfterTool(context.Background(), api.ToolCall{CallID: "child-call", Name: "shell", Arguments: json.RawMessage(`{}`)}, tools.ExecutionResult{}, nil)
	if call := <-calls; call.id != "observe" || call.envelope["sessionId"] != "child" {
		t.Fatalf("inherited dispatch=%#v", call)
	}

	late, _ := NewClientHookGroup("PreToolUse", "", []string{"late"}, 5*time.Millisecond)
	timed := AttachClient(nil, []ClientHookGroup{late}, func(ctx context.Context, _ string, _ map[string]any, _ bool) (string, string) {
		<-ctx.Done()
		return "deny", "too late"
	})
	if err := (&Runtime{Catalog: timed}).BeforeTool(context.Background(), api.ToolCall{Name: "shell"}); err != nil {
		t.Fatalf("timed-out client hook did not fail open: %v", err)
	}
	if _, ok := NewClientHookGroup("Unknown", "", []string{"x"}, 0); ok {
		t.Fatal("unknown event accepted")
	}
	if _, ok := NewClientHookGroup("PreToolUse", "[", []string{"x"}, 0); ok {
		t.Fatal("invalid matcher accepted")
	}
}

func TestHookPayloadsUseReferenceSizeBound(t *testing.T) {
	post, _ := NewClientHookGroup("PostToolUse", "", []string{"observe"}, 0)
	dispatched := make(chan map[string]any, 1)
	catalog := AttachClient(nil, []ClientHookGroup{post}, func(_ context.Context, _ string, envelope map[string]any, _ bool) (string, string) {
		dispatched <- envelope
		return "", ""
	})
	large := strings.Repeat("€", maxHookPayloadSize)
	arguments, _ := json.Marshal(map[string]string{"value": large})
	(&Runtime{Catalog: catalog}).AfterTool(context.Background(), api.ToolCall{Name: "shell", Arguments: arguments}, tools.ExecutionResult{Output: large}, nil)
	envelope := <-dispatched
	input, inputOK := envelope["toolInput"].(string)
	output, outputOK := envelope["toolResult"].(string)
	if envelope["toolInputTruncated"] != true || envelope["toolResultTruncated"] != true || !inputOK || !outputOK || !strings.HasSuffix(input, " [truncated]") || !strings.HasSuffix(output, " [truncated]") || len(input) > maxHookPayloadSize+len(" [truncated]") || len(output) > maxHookPayloadSize+len(" [truncated]") {
		t.Fatalf("input bytes=%d output bytes=%d envelope=%#v", len(input), len(output), envelope)
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
