package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lookcorner/go-cli/internal/workspace"
)

func TestParseSandboxProfile(t *testing.T) {
	for input, want := range map[string]SandboxProfile{
		"": SandboxOff, "off": SandboxOff, " WORKSPACE ": SandboxWorkspace, "read-only": SandboxReadOnly,
	} {
		got, err := ParseSandboxProfile(input)
		if err != nil || got != want {
			t.Fatalf("ParseSandboxProfile(%q)=%q, %v; want %q", input, got, err, want)
		}
	}
	if _, err := ParseSandboxProfile("strict"); err == nil {
		t.Fatal("unsupported profile was accepted")
	}
}

func TestSandboxOffLeavesCommandUnwrapped(t *testing.T) {
	path, args, err := sandboxInvocation("", "/workspace", "/bin/sh", []string{"-lc", "true"})
	if err != nil || path != "/bin/sh" || strings.Join(args, " ") != "-lc true" {
		t.Fatalf("path=%q args=%q err=%v", path, args, err)
	}
}

func TestSeatbeltPolicyScopesWorkspaceWrites(t *testing.T) {
	workspacePolicy, err := seatbeltPolicy(SandboxWorkspace, `/work/with "quote"`)
	if err != nil {
		t.Fatal(err)
	}
	readOnlyPolicy, err := seatbeltPolicy(SandboxReadOnly, "/work")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(workspacePolicy, `(subpath "/work/with \"quote\"")`) ||
		!strings.Contains(workspacePolicy, "(allow file-read*)") ||
		strings.Contains(readOnlyPolicy, `(subpath "/work")`) {
		t.Fatalf("workspace policy:\n%s\nread-only policy:\n%s", workspacePolicy, readOnlyPolicy)
	}
}

func TestWorkspaceSandboxCoversShellAndBackgroundCommands(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("kernel sandbox is Unix-specific")
	}
	if err := validateSandboxRuntime(SandboxWorkspace); err != nil {
		t.Skip(err)
	}
	root := userSandboxTempDir(t, ".gork-workspace-*")
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry(ws, PromptApprover{Mode: PermissionAuto})
	defer registry.Close()
	if err := registry.ConfigureSandbox("workspace"); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Execute(context.Background(), "shell", json.RawMessage(`{"command":"printf foreground > foreground.txt"}`)); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(root, "foreground.txt")); err != nil || string(data) != "foreground" {
		t.Fatalf("foreground data=%q err=%v", data, err)
	}
	id, err := registry.processes.Start(context.Background(), "printf background > background.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.processes.WaitOutput(context.Background(), id, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(root, "background.txt")); err != nil || string(data) != "background" {
		t.Fatalf("background data=%q err=%v", data, err)
	}
	childRoot := userSandboxTempDir(t, ".gork-child-*")
	childWorkspace, err := workspace.Open(childRoot)
	if err != nil {
		t.Fatal(err)
	}
	child := registry.ForWorkspace(childWorkspace)
	defer child.Close()
	child.processes.sandboxMu.RLock()
	processSandbox := child.processes.sandbox
	child.processes.sandboxMu.RUnlock()
	if child.sandbox != SandboxWorkspace || processSandbox != SandboxWorkspace {
		t.Fatalf("child sandbox=%q process sandbox=%q", child.sandbox, processSandbox)
	}
	if _, err := child.Execute(context.Background(), "shell", json.RawMessage(`{"command":"printf child > child.txt"}`)); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(childRoot, "child.txt")); err != nil || string(data) != "child" {
		t.Fatalf("child data=%q err=%v", data, err)
	}
}

func TestReadOnlySandboxDeniesWorkspaceWrite(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("kernel sandbox is Unix-specific")
	}
	if err := validateSandboxRuntime(SandboxReadOnly); err != nil {
		t.Skip(err)
	}
	root := userSandboxTempDir(t, ".gork-read-only-*")
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry(ws, PromptApprover{Mode: PermissionAuto})
	defer registry.Close()
	if err := registry.ConfigureSandbox("read-only"); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Execute(context.Background(), "shell", json.RawMessage(`{"command":"printf denied > denied.txt"}`)); err == nil {
		t.Fatal("read-only sandbox allowed a workspace write")
	}
	if _, err := os.Stat(filepath.Join(root, "denied.txt")); !os.IsNotExist(err) {
		t.Fatalf("denied path exists: %v", err)
	}
}

func userSandboxTempDir(t *testing.T, pattern string) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip(err)
	}
	root, err := os.MkdirTemp(home, pattern)
	if err != nil {
		t.Skip(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	return root
}

func TestDarwinWorkspaceSandboxDeniesHomeWrite(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Seatbelt-specific")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip(err)
	}
	target := filepath.Join(home, ".gork-sandbox-probe-"+time.Now().UTC().Format("20060102150405.000000000"))
	t.Cleanup(func() { _ = os.Remove(target) })
	cmd, err := sandboxCommand(context.Background(), SandboxWorkspace, t.TempDir(), "/bin/sh", "-lc", "printf denied > "+strconv.Quote(target))
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Run(); err == nil {
		t.Fatal("Seatbelt allowed a write outside the workspace")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("denied path exists: %v", err)
	}
}
