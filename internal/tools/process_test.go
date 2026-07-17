package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/lookcorner/go-cli/internal/workspace"
)

func TestGorkTerminalToolForegroundReportsExitCode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is Unix-specific")
	}
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	manager := NewProcessManager(ws, PromptApprover{Mode: PermissionAuto})
	defer manager.Close()
	output, err := (&runTerminalCommandTool{manager: manager}).Execute(context.Background(), json.RawMessage(
		`{"command":"printf hello; exit 7","description":"exercise exit status","is_background":false}`,
	))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "exit: 7") || !strings.Contains(output, "hello") {
		t.Fatalf("unexpected foreground output: %s", output)
	}
}

func TestGorkTerminalToolBackgroundTaskProtocol(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is Unix-specific")
	}
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	manager := NewProcessManager(ws, PromptApprover{Mode: PermissionAuto})
	defer manager.Close()
	started, err := (&runTerminalCommandTool{manager: manager}).Execute(context.Background(), json.RawMessage(
		`{"command":"sleep 0.05; printf done","description":"exercise background task","timeout":0,"is_background":true}`,
	))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(started, "task_1") || !strings.Contains(started, "get_task_output") {
		t.Fatalf("unexpected start output: %s", started)
	}
	output, err := (&taskOutputTool{manager: manager}).Execute(context.Background(), json.RawMessage(
		`{"task_ids":["task_1","task_1"],"timeout_ms":1000}`,
	))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "exited successfully") || !strings.Contains(output, "done") {
		t.Fatalf("unexpected task output: %s", output)
	}
}

func TestPersistentShellDirectoryAndEnvironment(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("persistent shell fixture is Unix-specific")
	}
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	manager := NewProcessManager(ws, PromptApprover{Mode: PermissionAuto})
	defer manager.Close()
	tool := &runTerminalCommandTool{manager: manager}
	if _, err := tool.Execute(context.Background(), json.RawMessage(
		`{"command":"cd nested; export GORK_STATE_TEST=preserved","description":"change shell state"}`,
	)); err != nil {
		t.Fatal(err)
	}
	output, err := tool.Execute(context.Background(), json.RawMessage(
		`{"command":"printf '%s|%s' \"$PWD\" \"$GORK_STATE_TEST\"","description":"read shell state"}`,
	))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, filepath.Join(root, "nested")+"|preserved") {
		t.Fatalf("shell state was not preserved: %s", output)
	}
	if _, err := tool.Execute(context.Background(), json.RawMessage(
		`{"command":"gork_state_fn() { printf function-ok; }; alias gork_state_alias='printf alias-ok'","description":"define shell function and alias"}`,
	)); err != nil {
		t.Fatal(err)
	}
	definitions, err := tool.Execute(context.Background(), json.RawMessage(
		`{"command":"gork_state_fn; printf '|'; gork_state_alias","description":"use shell function and alias"}`,
	))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(definitions, "function-ok|alias-ok") {
		t.Fatalf("shell definitions were not preserved: %s", definitions)
	}
	started, err := tool.Execute(context.Background(), json.RawMessage(
		`{"command":"printf '%s|%s' \"$PWD\" \"$GORK_STATE_TEST\"","description":"inherit state in background","is_background":true}`,
	))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(started, "task_1") {
		t.Fatalf("unexpected task: %s", started)
	}
	background, err := manager.WaitOutput(context.Background(), "task_1", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(background, filepath.Join(root, "nested")+"|preserved") {
		t.Fatalf("background task did not inherit shell state: %s", background)
	}
}

func TestBackgroundCommandLifecycle(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is Unix-specific")
	}
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	manager := NewProcessManager(ws, PromptApprover{Mode: PermissionAuto})
	defer manager.Close()
	id, err := manager.Start(context.Background(), "printf hello")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		output, err := manager.Output(id)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(output, "exited successfully") {
			if !strings.Contains(output, "hello") {
				t.Fatalf("missing command output: %s", output)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("command did not complete: %s", output)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestBackgroundCommandKill(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is Unix-specific")
	}
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	manager := NewProcessManager(ws, PromptApprover{Mode: PermissionAuto})
	defer manager.Close()
	id, err := manager.Start(context.Background(), "sleep 30")
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Kill(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	output, err := manager.Output(id)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output, "status: running") {
		t.Fatalf("process still running: %s", output)
	}
}
