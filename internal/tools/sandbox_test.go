package tools

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lookcorner/go-cli/internal/workspace"
)

func TestParseSandboxProfile(t *testing.T) {
	for input, want := range map[string]SandboxProfile{
		"": SandboxOff, "off": SandboxOff, "none": SandboxOff, " WORKSPACE ": SandboxWorkspace, "read-only": SandboxReadOnly, "readonly": SandboxReadOnly, "STRICT": SandboxStrict,
	} {
		got, err := ParseSandboxProfile(input)
		if err != nil || got != want {
			t.Fatalf("ParseSandboxProfile(%q)=%q, %v; want %q", input, got, err, want)
		}
	}
	got, err := ParseSandboxProfile("project")
	if err != nil || got != SandboxProfile("project") {
		t.Fatalf("custom ParseSandboxProfile: %q %v", got, err)
	}
	if _, err := ParseSandboxProfile("bad name"); err == nil {
		t.Fatal("invalid custom name was accepted")
	}
}

func TestSandboxOffLeavesCommandUnwrapped(t *testing.T) {
	path, args, err := sandboxInvocation("", false, "/workspace", "/bin/sh", []string{"-lc", "true"})
	if err != nil || path != "/bin/sh" || strings.Join(args, " ") != "-lc true" {
		t.Fatalf("path=%q args=%q err=%v", path, args, err)
	}
}

func TestSeatbeltPolicyScopesWorkspaceWrites(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("seatbelt policies target macOS paths")
	}
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

func TestStrictSeatbeltPolicyScopesReadsAndNetwork(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("seatbelt policies target macOS paths")
	}
	workspace := t.TempDir()
	policy, err := seatbeltPolicy(SandboxStrict, workspace)
	if err != nil {
		t.Fatal(err)
	}
	realWorkspace, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(policy, "(allow file-read*)") || strings.Contains(policy, "(allow network*)") ||
		!strings.Contains(policy, `(allow file-read* (literal "/") `) || !strings.Contains(policy, `(subpath "`+realWorkspace+`")`) {
		t.Fatalf("strict policy:\n%s", policy)
	}
}

func TestBubblewrapProfilesAndStrictParents(t *testing.T) {
	workspace := t.TempDir()
	strict := bubblewrapArgs(SandboxStrict, true, workspace, "/bin/sh")
	strictText := strings.Join(strict, " ")
	if !strings.Contains(strictText, "--tmpfs / ") || !strings.Contains(strictText, "--unshare-net") ||
		!hasSandboxArgPair(strict, "--ro-bind", workspace, workspace) || !hasSandboxArgPair(strict, "--bind", workspace, workspace) {
		t.Fatalf("strict args=%q", strict)
	}
	remount, command := slices.Index(strict, "--remount-ro"), slices.Index(strict, "--")
	lastBind := slices.Index(strict, "--bind")
	for index, value := range strict {
		if value == "--bind" {
			lastBind = index
		}
	}
	if remount <= lastBind || command <= remount || remount+1 >= len(strict) || strict[remount+1] != "/" {
		t.Fatalf("strict root remount order=%q", strict)
	}
	for parent := filepath.Dir(workspace); parent != "/"; parent = filepath.Dir(parent) {
		if !hasSandboxArgPair(strict, "--dir", parent) {
			t.Fatalf("strict args missing parent %q: %q", parent, strict)
		}
	}
	readOnlyText := strings.Join(bubblewrapArgs(SandboxReadOnly, true, workspace, "/bin/sh"), " ")
	if !strings.Contains(readOnlyText, "--ro-bind / / ") || !strings.Contains(readOnlyText, "--unshare-net") {
		t.Fatalf("read-only args=%q", readOnlyText)
	}
	workspaceText := strings.Join(bubblewrapArgs(SandboxWorkspace, false, workspace, "/bin/sh"), " ")
	if strings.Contains(workspaceText, "--unshare-net") || !strings.Contains(workspaceText, "--ro-bind / / ") {
		t.Fatalf("workspace args=%q", workspaceText)
	}
}

func hasSandboxArgPair(args []string, values ...string) bool {
	for index := 0; index+len(values) <= len(args); index++ {
		if slices.Equal(args[index:index+len(values)], values) {
			return true
		}
	}
	return false
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

func TestStrictSandboxAllowsWorkspaceAndDeniesHomeRead(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("strict sandbox requires macOS Seatbelt or Linux bubblewrap")
	}
	if err := validateSandboxRuntime(SandboxStrict); err != nil {
		t.Skip(err)
	}
	root := userSandboxTempDir(t, ".gork-strict-workspace-*")
	workspaceFile := filepath.Join(root, "inside.txt")
	if err := os.WriteFile(workspaceFile, []byte("inside"), 0o600); err != nil {
		t.Fatal(err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip(err)
	}
	outside, err := os.CreateTemp(home, ".gork-strict-outside-*")
	if err != nil {
		t.Skip(err)
	}
	outsidePath := outside.Name()
	if _, err := outside.WriteString("outside"); err != nil {
		_ = outside.Close()
		t.Fatal(err)
	}
	if err := outside.Close(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(outsidePath) })
	command, err := sandboxCommand(context.Background(), SandboxStrict, true, root, "/bin/sh", "-lc",
		"cat inside.txt && printf written > created.txt && cat "+strconv.Quote(outsidePath))
	if err != nil {
		t.Fatal(err)
	}
	command.Dir = root
	output, runErr := command.CombinedOutput()
	if runErr == nil {
		t.Fatalf("strict sandbox read outside home: %s", output)
	}
	if !strings.Contains(string(output), "inside") {
		t.Fatalf("strict sandbox could not read workspace: %s (%v)", output, runErr)
	}
	if data, err := os.ReadFile(filepath.Join(root, "created.txt")); err != nil || string(data) != "written" {
		t.Fatalf("strict workspace write=%q err=%v", data, err)
	}
}

func TestDarwinStrictSandboxDeniesNetwork(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS-specific")
	}
	if err := validateSandboxRuntime(SandboxStrict); err != nil {
		t.Skip(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	address := listener.Addr().(*net.TCPAddr)
	cmd, err := sandboxCommand(context.Background(), SandboxStrict, true, t.TempDir(), "/usr/bin/nc", "-z", "127.0.0.1", strconv.Itoa(address.Port))
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Run(); err == nil {
		t.Fatal("strict Seatbelt allowed network access")
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
	cmd, err := sandboxCommand(context.Background(), SandboxWorkspace, false, t.TempDir(), "/bin/sh", "-lc", "printf denied > "+strconv.Quote(target))
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
