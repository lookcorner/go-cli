package tools

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
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
	root := t.TempDir()
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	store, err := workspace.NewRewindStore(ws, filepath.Join(t.TempDir(), "rewind.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	manager := NewProcessManager(ws, PromptApprover{Mode: PermissionAuto})
	manager.rewind = &mutationCheckpoint{store: store, promptIndex: func() int { return 0 }}
	defer manager.Close()
	started, err := (&runTerminalCommandTool{manager: manager}).Execute(context.Background(), json.RawMessage(
		`{"command":"sleep 0.05; printf done; printf created > made.txt","description":"exercise background task","timeout":0,"is_background":true}`,
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
	counts, err := store.Counts()
	if err != nil || counts[0] != 1 {
		t.Fatalf("unexpected background checkpoints: %#v err=%v", counts, err)
	}
	if _, _, err := store.Restore(0); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "made.txt")); !os.IsNotExist(err) {
		t.Fatalf("background-created file remained: %v", err)
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

func TestPersistentBashOptionsAndNounsetReset(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is Unix-specific")
	}
	t.Setenv("SHELL", "/bin/bash")
	t.Setenv("GROK_SHELL", "")
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	manager := NewProcessManager(ws, PromptApprover{Mode: PermissionAuto})
	defer manager.Close()
	tool := &runTerminalCommandTool{manager: manager}
	if _, err := tool.Execute(context.Background(), json.RawMessage(
		`{"command":"set -o pipefail; shopt -s nullglob; set -u","description":"persist bash options"}`,
	)); err != nil {
		t.Fatal(err)
	}
	output, err := tool.Execute(context.Background(), json.RawMessage(
		`{"command":"set -o | grep '^pipefail[[:space:]]*on'; shopt -q nullglob && printf 'nullglob-on|'; printf '<%s>' \"${GORK_MISSING_TEST-}\"","description":"read bash options"}`,
	))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "pipefail") || !strings.Contains(output, "nullglob-on|<>") {
		t.Fatalf("bash options did not persist or nounset was not reset: %s", output)
	}
	if _, err := tool.Execute(context.Background(), json.RawMessage(
		`{"command":"set -e","description":"persist bash errexit"}`,
	)); err != nil {
		t.Fatal(err)
	}
	errexit, err := tool.Execute(context.Background(), json.RawMessage(
		`{"command":"false; printf should-not-run","description":"exercise persisted errexit"}`,
	))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errexit, "exit: 1") || strings.Contains(errexit, "should-not-run") {
		t.Fatalf("bash errexit did not persist: %s", errexit)
	}
}

func TestPersistentZshOptionsAndNonomatchReset(t *testing.T) {
	if runtime.GOOS == "windows" || func() bool { _, err := exec.LookPath("zsh"); return err != nil }() {
		t.Skip("zsh is unavailable")
	}
	t.Setenv("SHELL", "/bin/zsh")
	t.Setenv("GROK_SHELL", "")
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	manager := NewProcessManager(ws, PromptApprover{Mode: PermissionAuto})
	defer manager.Close()
	tool := &runTerminalCommandTool{manager: manager}
	if _, err := tool.Execute(context.Background(), json.RawMessage(
		`{"command":"setopt extendedglob; setopt nounset","description":"persist zsh options"}`,
	)); err != nil {
		t.Fatal(err)
	}
	output, err := tool.Execute(context.Background(), json.RawMessage(
		`{"command":"[[ -o extendedglob ]] && print -n 'extendedglob-on|'; print -n \"${GORK_ZSH_MISSING-}|\"; print -r -- gork-no-match-*.zzz","description":"read zsh options"}`,
	))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "extendedglob-on||gork-no-match-*.zzz") {
		t.Fatalf("zsh options did not persist or nounset/nonomatch behavior was wrong: %s", output)
	}
}

func TestSelectedShellPrefersGrokOverride(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is Unix-specific")
	}
	t.Setenv("SHELL", "/bin/zsh")
	t.Setenv("GROK_SHELL", "/bin/bash")
	if shell := selectedShell(); filepath.Base(shell) != "bash" {
		t.Fatalf("GROK_SHELL override was not selected: %s", shell)
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

func TestRegistryBackgroundTaskSnapshotsAndKillOutcomes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is Unix-specific")
	}
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry(ws, PromptApprover{Mode: PermissionAuto})
	defer registry.Close()
	completedID, err := registry.processes.Start(context.Background(), "printf hello")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.processes.WaitOutput(context.Background(), completedID, time.Second); err != nil {
		t.Fatal(err)
	}
	snapshots := registry.BackgroundTasks()
	if len(snapshots) != 1 || snapshots[0].TaskID != completedID || snapshots[0].Command != "printf hello" || snapshots[0].Output != "hello" || !snapshots[0].Completed || snapshots[0].ExitCode == nil || *snapshots[0].ExitCode != 0 || snapshots[0].EndTime == nil {
		t.Fatalf("snapshots=%#v", snapshots)
	}
	if outcome, err := registry.KillBackgroundTask(context.Background(), completedID); err != nil || outcome != "already_exited" {
		t.Fatalf("completed kill outcome=%q err=%v", outcome, err)
	}
	runningID, err := registry.processes.Start(context.Background(), "sleep 30")
	if err != nil {
		t.Fatal(err)
	}
	if outcome, err := registry.KillBackgroundTask(context.Background(), runningID); err != nil || outcome != "killed" {
		t.Fatalf("running kill outcome=%q err=%v", outcome, err)
	}
	if outcome, err := registry.KillBackgroundTask(context.Background(), "missing"); err != nil || outcome != "not_found" {
		t.Fatalf("missing kill outcome=%q err=%v", outcome, err)
	}
	snapshots = registry.BackgroundTasks()
	if len(snapshots) != 2 || !snapshots[1].ExplicitlyKilled {
		t.Fatalf("killed snapshot=%#v", snapshots)
	}
}
