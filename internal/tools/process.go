package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
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
	ws        *workspace.Workspace
	approver  Approver
	nextID    atomic.Uint64
	mu        sync.Mutex
	processes map[string]*backgroundProcess
	closed    bool
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
	return &ProcessManager{ws: ws, approver: approver, processes: make(map[string]*backgroundProcess)}
}

func (m *ProcessManager) Start(ctx context.Context, command string) (string, error) {
	if strings.TrimSpace(command) == "" {
		return "", errors.New("command must not be empty")
	}
	if err := m.approver.Approve(ctx, "start background command", command); err != nil {
		return "", err
	}
	cmd := shellCommand(command)
	cmd.Dir = m.ws.Root()
	configureProcessGroup(cmd)
	buffer := &tailBuffer{limit: backgroundOutputBytes}
	cmd.Stdout = buffer
	cmd.Stderr = buffer
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start command: %w", err)
	}
	id := fmt.Sprintf("cmd_%d", m.nextID.Add(1))
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
		process.mu.Lock()
		process.err = err
		process.mu.Unlock()
		close(process.done)
	}()
	return id, nil
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
	return exec.Command("/bin/sh", "-lc", command)
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
