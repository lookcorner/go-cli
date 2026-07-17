package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lookcorner/go-cli/internal/api"
	"github.com/lookcorner/go-cli/internal/workspace"
)

const backgroundOutputBytes = 1 << 20

type ProcessManager struct {
	ws           *workspace.Workspace
	approver     Approver
	nextID       atomic.Uint64
	mu           sync.Mutex
	processes    map[string]*backgroundProcess
	closed       bool
	stateMu      sync.Mutex
	currentDir   string
	environment  []string
	shellPrelude string
	rewind       *mutationCheckpoint
}

type backgroundProcess struct {
	id      string
	command string
	cmd     *exec.Cmd
	output  *tailBuffer
	started time.Time
	done    chan struct{}
	mu      sync.Mutex
	err     error
}

func NewProcessManager(ws *workspace.Workspace, approver Approver) *ProcessManager {
	return &ProcessManager{
		ws: ws, approver: approver, processes: make(map[string]*backgroundProcess),
		currentDir: ws.Root(), environment: os.Environ(),
	}
}

func (m *ProcessManager) Start(ctx context.Context, command string) (string, error) {
	return m.StartWithTimeout(ctx, command, 0)
}

func (m *ProcessManager) StartWithTimeout(ctx context.Context, command string, timeout time.Duration) (string, error) {
	if strings.TrimSpace(command) == "" {
		return "", errors.New("command must not be empty")
	}
	if err := m.approver.Approve(ctx, "start background command", command); err != nil {
		return "", err
	}
	checkpoint, err := m.rewind.beforeWorkspace()
	if err != nil {
		return "", fmt.Errorf("checkpoint before background command: %w", err)
	}
	cmd := shellCommand(command)
	cmd.Dir, cmd.Env = m.shellSnapshot()
	configureProcessGroup(cmd)
	buffer := &tailBuffer{limit: backgroundOutputBytes}
	cmd.Stdout = buffer
	cmd.Stderr = buffer
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start command: %w", err)
	}
	id := fmt.Sprintf("task_%d", m.nextID.Add(1))
	process := &backgroundProcess{
		id: id, command: command, cmd: cmd, output: buffer, started: time.Now(), done: make(chan struct{}),
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		_ = terminateProcess(cmd)
		_ = cmd.Wait()
		return "", errors.New("process manager is closed")
	}
	m.processes[id] = process
	m.mu.Unlock()
	go func() {
		err := cmd.Wait()
		if checkpointErr := m.rewind.afterWorkspace(checkpoint); checkpointErr != nil {
			err = errors.Join(err, fmt.Errorf("checkpoint after background command: %w", checkpointErr))
		}
		process.mu.Lock()
		process.err = err
		process.mu.Unlock()
		close(process.done)
	}()
	if timeout > 0 {
		go func() {
			timer := time.NewTimer(timeout)
			defer timer.Stop()
			select {
			case <-process.done:
				return
			case <-timer.C:
				_ = terminateProcess(cmd)
				select {
				case <-process.done:
				case <-time.After(time.Second):
					_ = forceKillProcess(cmd)
				}
			}
		}()
	}
	return id, nil
}

func (m *ProcessManager) RunForeground(ctx context.Context, command string, timeout time.Duration) (string, error) {
	if strings.TrimSpace(command) == "" {
		return "", errors.New("command must not be empty")
	}
	if err := m.approver.Approve(ctx, "run terminal command", command); err != nil {
		return "", err
	}
	checkpoint, err := m.rewind.beforeWorkspace()
	if err != nil {
		return "", fmt.Errorf("checkpoint before terminal command: %w", err)
	}
	m.stateMu.Lock()
	defer m.stateMu.Unlock()
	cmd, capture, err := m.persistentShellCommand(command)
	if err != nil {
		return "", err
	}
	defer capture.cleanup()
	configureProcessGroup(cmd)
	buffer := &tailBuffer{limit: backgroundOutputBytes}
	cmd.Stdout = buffer
	cmd.Stderr = buffer
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start command: %w", err)
	}
	wait := make(chan error, 1)
	go func() { wait <- cmd.Wait() }()
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	var waitErr error
	status := "0"
	select {
	case waitErr = <-wait:
		if waitErr != nil {
			status = exitStatus(waitErr)
		}
	case <-timer.C:
		status = "killed (timeout)"
		waitErr = terminateAndWait(cmd, wait)
	case <-ctx.Done():
		status = "killed (cancelled)"
		waitErr = terminateAndWait(cmd, wait)
	}
	_ = waitErr
	m.applyShellCapture(capture)
	checkpointErr := m.rewind.afterWorkspace(checkpoint)
	output := strings.TrimSpace(buffer.String())
	if checkpointErr != nil {
		return output, fmt.Errorf("checkpoint after terminal command: %w", checkpointErr)
	}
	if output == "" {
		return "exit: " + status, nil
	}
	return "exit: " + status + "\n" + output, nil
}

type shellCapture struct {
	cwdPath     string
	envPath     string
	scriptPath  string
	commandPath string
}

func (c shellCapture) cleanup() {
	_ = os.Remove(c.cwdPath)
	_ = os.Remove(c.envPath)
	_ = os.Remove(c.scriptPath)
	_ = os.Remove(c.commandPath)
}

func (m *ProcessManager) shellSnapshot() (string, []string) {
	m.stateMu.Lock()
	defer m.stateMu.Unlock()
	return m.currentDir, setEnvironment(m.environment, map[string]string{
		"GORK_AGENT": "1", "TERM": "dumb", "NO_COLOR": "1", "FORCE_COLOR": "0",
	})
}

func (m *ProcessManager) persistentShellCommand(command string) (*exec.Cmd, shellCapture, error) {
	capture := shellCapture{}
	if runtime.GOOS == "windows" {
		cmd := shellCommand(command)
		cmd.Dir = m.currentDir
		cmd.Env = append([]string(nil), m.environment...)
		return cmd, capture, nil
	}
	cwdFile, err := os.CreateTemp("", "gork-shell-cwd-*")
	if err != nil {
		return nil, capture, fmt.Errorf("create shell cwd capture: %w", err)
	}
	capture.cwdPath = cwdFile.Name()
	if err := cwdFile.Close(); err != nil {
		capture.cleanup()
		return nil, capture, err
	}
	envFile, err := os.CreateTemp("", "gork-shell-env-*")
	if err != nil {
		capture.cleanup()
		return nil, capture, fmt.Errorf("create shell environment capture: %w", err)
	}
	capture.envPath = envFile.Name()
	if err := envFile.Close(); err != nil {
		capture.cleanup()
		return nil, capture, err
	}
	scriptFile, err := os.CreateTemp("", "gork-shell-script-*")
	if err != nil {
		capture.cleanup()
		return nil, capture, fmt.Errorf("create shell script capture: %w", err)
	}
	capture.scriptPath = scriptFile.Name()
	if err := scriptFile.Close(); err != nil {
		capture.cleanup()
		return nil, capture, err
	}
	commandFile, err := os.CreateTemp("", "gork-shell-command-*")
	if err != nil {
		capture.cleanup()
		return nil, capture, fmt.Errorf("create shell command file: %w", err)
	}
	capture.commandPath = commandFile.Name()
	if _, err := commandFile.WriteString(command); err != nil {
		_ = commandFile.Close()
		capture.cleanup()
		return nil, capture, fmt.Errorf("write shell command file: %w", err)
	}
	if err := commandFile.Close(); err != nil {
		capture.cleanup()
		return nil, capture, err
	}
	dumpScript := ":"
	bootstrap := ""
	switch filepath.Base(selectedShell()) {
	case "bash":
		dumpScript = `{ set +o 2>/dev/null | command grep -v 'nounset' || true; shopt -p 2>/dev/null || true; declare -f 2>/dev/null || true; alias -p 2>/dev/null || true; }`
		bootstrap = "shopt -s expand_aliases\n"
	case "zsh":
		dumpScript = `{ setopt 2>/dev/null | command grep -v "^nounset$" | command sed -e "s/^/setopt /" || true; typeset -f 2>/dev/null || true; { alias -L; alias -gL; alias -sL; } 2>/dev/null || true; }`
		bootstrap = "unsetopt nounset 2>/dev/null || true\nsetopt nonomatch aliases 2>/dev/null || true\n"
	}
	trap := "trap '__gork_status=$?; pwd > \"$GORK_GO_STATE_CWD\"; /usr/bin/env -0 > \"$GORK_GO_STATE_ENV\"; " + dumpScript + " > \"$GORK_GO_STATE_SCRIPT\"; trap - EXIT; exit \"$__gork_status\"' EXIT\n"
	wrapped := m.shellPrelude + "\n" + bootstrap + trap + ". \"$GORK_GO_COMMAND_FILE\""
	cmd := shellCommand(wrapped)
	cmd.Dir = m.currentDir
	cmd.Env = setEnvironment(m.environment, map[string]string{
		"GORK_GO_STATE_CWD":    capture.cwdPath,
		"GORK_GO_STATE_ENV":    capture.envPath,
		"GORK_GO_STATE_SCRIPT": capture.scriptPath,
		"GORK_GO_COMMAND_FILE": capture.commandPath,
		"GORK_AGENT":           "1",
		"TERM":                 "dumb",
		"NO_COLOR":             "1",
		"FORCE_COLOR":          "0",
	})
	return cmd, capture, nil
}

func (m *ProcessManager) applyShellCapture(capture shellCapture) {
	if capture.cwdPath == "" || capture.envPath == "" {
		return
	}
	if data, err := readShellCapture(capture.cwdPath, 64<<10); err == nil {
		cwd := strings.TrimSpace(string(data))
		if info, statErr := os.Stat(cwd); cwd != "" && statErr == nil && info.IsDir() {
			m.currentDir = cwd
		}
	}
	if data, err := readShellCapture(capture.envPath, 4<<20); err == nil {
		entries := strings.Split(string(data), "\x00")
		filtered := make([]string, 0, len(entries))
		for _, entry := range entries {
			if entry == "" || strings.HasPrefix(entry, "GORK_GO_STATE_CWD=") || strings.HasPrefix(entry, "GORK_GO_STATE_ENV=") || strings.HasPrefix(entry, "GORK_GO_STATE_SCRIPT=") || strings.HasPrefix(entry, "GORK_GO_COMMAND_FILE=") {
				continue
			}
			filtered = append(filtered, entry)
		}
		if len(filtered) > 0 {
			m.environment = filtered
		}
	}
	if data, err := readShellCapture(capture.scriptPath, 4<<20); err == nil {
		m.shellPrelude = string(data)
	}
}

func readShellCapture(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("shell state capture exceeds size limit")
	}
	return data, nil
}

func setEnvironment(base []string, values map[string]string) []string {
	result := append([]string(nil), base...)
	indexes := make(map[string]int, len(result))
	for index, entry := range result {
		if key, _, ok := strings.Cut(entry, "="); ok {
			indexes[key] = index
		}
	}
	for key, value := range values {
		entry := key + "=" + value
		if index, exists := indexes[key]; exists {
			result[index] = entry
		} else {
			indexes[key] = len(result)
			result = append(result, entry)
		}
	}
	return result
}

func terminateAndWait(cmd *exec.Cmd, wait <-chan error) error {
	_ = terminateProcess(cmd)
	select {
	case err := <-wait:
		return err
	case <-time.After(time.Second):
		_ = forceKillProcess(cmd)
		return <-wait
	}
}

func exitStatus(err error) string {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return fmt.Sprintf("%d", exitErr.ExitCode())
	}
	if errors.Is(err, os.ErrProcessDone) {
		return "0"
	}
	return "1"
}

func (m *ProcessManager) Output(id string) (string, error) {
	process, err := m.lookup(id)
	if err != nil {
		return "", err
	}
	status := "running"
	select {
	case <-process.done:
		process.mu.Lock()
		exitErr := process.err
		process.mu.Unlock()
		if exitErr != nil {
			status = "exited with error: " + exitErr.Error()
		} else {
			status = "exited successfully"
		}
	default:
	}
	return fmt.Sprintf("id: %s\nstatus: %s\nruntime: %s\ncommand: %s\n\n%s",
		process.id, status, time.Since(process.started).Round(time.Millisecond), process.command, process.output.String()), nil
}

func (m *ProcessManager) WaitOutput(ctx context.Context, id string, timeout time.Duration) (string, error) {
	process, err := m.lookup(id)
	if err != nil {
		return "", err
	}
	if timeout > 0 {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		select {
		case <-process.done:
		case <-timer.C:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return m.Output(id)
}

func (m *ProcessManager) Kill(ctx context.Context, id string) error {
	process, err := m.lookup(id)
	if err != nil {
		return err
	}
	select {
	case <-process.done:
		return nil
	default:
	}
	if err := m.approver.Approve(ctx, "kill background command", id+": "+process.command); err != nil {
		return err
	}
	if err := terminateProcess(process.cmd); err != nil {
		return fmt.Errorf("terminate %s: %w", id, err)
	}
	select {
	case <-process.done:
		return nil
	case <-time.After(3 * time.Second):
		if err := forceKillProcess(process.cmd); err != nil {
			return fmt.Errorf("force-kill %s: %w", id, err)
		}
		select {
		case <-process.done:
			return nil
		case <-time.After(2 * time.Second):
			return fmt.Errorf("process %s did not exit after kill", id)
		}
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *ProcessManager) List() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := make([]string, 0, len(m.processes))
	for id := range m.processes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func (m *ProcessManager) lookup(id string) (*backgroundProcess, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	process := m.processes[id]
	if process == nil {
		return nil, fmt.Errorf("unknown background command %q", id)
	}
	return process, nil
}

func (m *ProcessManager) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	processes := make([]*backgroundProcess, 0, len(m.processes))
	for _, process := range m.processes {
		processes = append(processes, process)
	}
	m.mu.Unlock()
	var failures []string
	for _, process := range processes {
		select {
		case <-process.done:
			continue
		default:
		}
		if err := terminateProcess(process.cmd); err != nil {
			failures = append(failures, process.id+": "+err.Error())
			continue
		}
		select {
		case <-process.done:
		case <-time.After(time.Second):
			if err := forceKillProcess(process.cmd); err != nil {
				failures = append(failures, process.id+": "+err.Error())
			}
		}
	}
	if len(failures) > 0 {
		return errors.New(strings.Join(failures, "; "))
	}
	return nil
}

func shellCommand(command string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		return exec.Command("cmd.exe", "/C", command)
	}
	return exec.Command(selectedShell(), "-lc", command)
}

func selectedShell() string {
	for _, candidate := range []string{os.Getenv("GROK_SHELL"), os.Getenv("SHELL")} {
		base := filepath.Base(candidate)
		if (base == "bash" || base == "zsh") && candidate != "" {
			if resolved, err := exec.LookPath(candidate); err == nil {
				return resolved
			}
		}
	}
	if resolved, err := exec.LookPath("bash"); err == nil {
		return resolved
	}
	return "/bin/bash"
}

type tailBuffer struct {
	mu        sync.Mutex
	data      []byte
	limit     int
	truncated bool
}

func (b *tailBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	original := len(data)
	b.data = append(b.data, data...)
	if len(b.data) > b.limit {
		b.data = append([]byte(nil), b.data[len(b.data)-b.limit:]...)
		b.truncated = true
	}
	return original, nil
}

func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.truncated {
		return "[earlier output truncated]\n" + string(b.data)
	}
	return string(b.data)
}

type startCommandTool struct{ manager *ProcessManager }

func (t *startCommandTool) Definition() api.ToolDefinition {
	return api.ToolDefinition{
		Type: "function", Name: "start_background_command",
		Description: "Start a long-running shell command in the workspace and return a command ID immediately.",
		Parameters:  objectSchema(map[string]any{"command": map[string]any{"type": "string"}}, "command"),
	}
}

func (t *startCommandTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("decode start command arguments: %w", err)
	}
	id, err := t.manager.Start(ctx, args.Command)
	if err != nil {
		return "", err
	}
	return "started background command " + id, nil
}

type commandOutputTool struct{ manager *ProcessManager }

func (t *commandOutputTool) Definition() api.ToolDefinition {
	return api.ToolDefinition{
		Type: "function", Name: "get_background_command_output",
		Description: "Read current output and status for a background command ID.",
		Parameters:  objectSchema(map[string]any{"id": map[string]any{"type": "string"}}, "id"),
	}
}

func (t *commandOutputTool) Execute(_ context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("decode command output arguments: %w", err)
	}
	return t.manager.Output(args.ID)
}

type killCommandTool struct{ manager *ProcessManager }

func (t *killCommandTool) Definition() api.ToolDefinition {
	return api.ToolDefinition{
		Type: "function", Name: "kill_background_command",
		Description: "Terminate a background command and its child process group.",
		Parameters:  objectSchema(map[string]any{"id": map[string]any{"type": "string"}}, "id"),
	}
}

type runTerminalCommandTool struct{ manager *ProcessManager }

func (t *runTerminalCommandTool) Definition() api.ToolDefinition {
	return api.ToolDefinition{
		Type: "function", Name: "run_terminal_cmd",
		Description: "Run a terminal command in the workspace. Set is_background for long-running commands; use get_task_output and kill_task with the returned task_id.",
		Parameters: objectSchema(map[string]any{
			"command":       map[string]any{"type": "string", "description": "The shell command to run."},
			"description":   map[string]any{"type": "string", "description": "One sentence explaining why the command is needed."},
			"timeout":       map[string]any{"type": "integer", "minimum": 0, "maximum": 300000, "description": "Timeout in milliseconds; default 120000."},
			"is_background": map[string]any{"type": "boolean", "description": "Run in the background and return a task_id immediately."},
		}, "command", "description"),
	}
}

func (t *runTerminalCommandTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		Command      string  `json:"command"`
		Description  string  `json:"description"`
		Timeout      *uint64 `json:"timeout"`
		IsBackground bool    `json:"is_background"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("decode run_terminal_cmd arguments: %w", err)
	}
	timeout := 2 * time.Minute
	if args.IsBackground {
		timeout = 0
	}
	if args.Timeout != nil {
		timeout = time.Duration(min(*args.Timeout, uint64(300000))) * time.Millisecond
	}
	if args.IsBackground {
		id, err := t.manager.StartWithTimeout(ctx, args.Command, timeout)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("Background task %s started\nUse get_task_output with task_ids=[%q] to retrieve the output.", id, id), nil
	}
	return t.manager.RunForeground(ctx, args.Command, timeout)
}

type taskOutputTool struct{ manager *ProcessManager }

func (t *taskOutputTool) Definition() api.ToolDefinition {
	return api.ToolDefinition{
		Type: "function", Name: "get_task_output",
		Description: "Get output and status from one or more background tasks. A positive timeout_ms waits for completion; omit it or pass 0 to poll.",
		Parameters: objectSchema(map[string]any{
			"task_ids":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "minItems": 1, "maxItems": 20},
			"timeout_ms": map[string]any{"type": "integer", "minimum": 0},
		}, "task_ids"),
	}
}

func (t *taskOutputTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		TaskIDs   []string `json:"task_ids"`
		TimeoutMS uint64   `json:"timeout_ms"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("decode get_task_output arguments: %w", err)
	}
	ids := uniqueTaskIDs(args.TaskIDs)
	if len(ids) == 0 {
		return "", errors.New("provide a non-empty task_ids list")
	}
	if len(ids) > 20 {
		return "", errors.New("task_ids exceeds maximum of 20 entries")
	}
	deadline := time.Now().Add(time.Duration(min(args.TimeoutMS, uint64(600000))) * time.Millisecond)
	outputs := make([]string, 0, len(ids))
	for _, id := range ids {
		var remaining time.Duration
		if args.TimeoutMS > 0 {
			remaining = time.Until(deadline)
			if remaining < 0 {
				remaining = 0
			}
		}
		output, err := t.manager.WaitOutput(ctx, id, remaining)
		if err != nil {
			return "", err
		}
		outputs = append(outputs, output)
	}
	return strings.Join(outputs, "\n\n"), nil
}

func uniqueTaskIDs(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
	result := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, exists := seen[id]; !exists {
			seen[id] = struct{}{}
			result = append(result, id)
		}
	}
	return result
}

type killTaskTool struct{ manager *ProcessManager }

func (t *killTaskTool) Definition() api.ToolDefinition {
	return api.ToolDefinition{
		Type: "function", Name: "kill_task",
		Description: "Terminate a running background task by task_id.",
		Parameters:  objectSchema(map[string]any{"task_id": map[string]any{"type": "string"}}, "task_id"),
	}
}

func (t *killTaskTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("decode kill_task arguments: %w", err)
	}
	process, err := t.manager.lookup(args.TaskID)
	if err != nil {
		return "", err
	}
	select {
	case <-process.done:
		return fmt.Sprintf("task_id: %s\noutcome: already_exited\nmessage: Task had already completed", args.TaskID), nil
	default:
	}
	if err := t.manager.Kill(ctx, args.TaskID); err != nil {
		return "", err
	}
	return fmt.Sprintf("task_id: %s\noutcome: killed\nmessage: Task was terminated successfully", args.TaskID), nil
}

func (t *killCommandTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("decode kill command arguments: %w", err)
	}
	if err := t.manager.Kill(ctx, args.ID); err != nil {
		return "", err
	}
	return "terminated background command " + args.ID, nil
}
