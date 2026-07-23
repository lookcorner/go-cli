package tools

import (
	"bytes"
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
	"unicode/utf8"

	"github.com/lookcorner/go-cli/internal/api"
	"github.com/lookcorner/go-cli/internal/workspace"
)

const (
	backgroundOutputBytes = 1 << 20
	monitorLineBytes      = 500
	monitorBatchBytes     = 3000
	monitorDebounce       = 200 * time.Millisecond
	monitorRateCapacity   = 10
	monitorRateRefill     = 2 * time.Second
	monitorOverloadLimit  = 30 * time.Second
)

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
	observerMu   sync.RWMutex
	observer     ProcessObserver
}

type ProcessObserver interface {
	TaskBackgrounded(ProcessBackgrounded)
	TaskCompleted(ProcessSnapshot)
}

type ProcessConsumptionObserver interface {
	TaskConsumed(string)
}

type ProcessMonitorObserver interface {
	MonitorEvent(MonitorEvent)
}

type MonitorEvent struct {
	TaskID      string `json:"task_id"`
	Description string `json:"description"`
	EventText   string `json:"event_text"`
}

type ProcessBackgrounded struct {
	ToolCallID  string
	TaskID      string
	Command     string
	CWD         string
	OutputFile  string
	Description string
	Kind        string
}

type backgroundProcess struct {
	id          string
	command     string
	description string
	cmd         *exec.Cmd
	output      *tailBuffer
	started     time.Time
	done        chan struct{}
	mu          sync.Mutex
	err         error
	ended       time.Time
	killed      bool
	terminal    bool
	blockWaited bool
	consumed    bool
	kind        string
	monitor     *monitorWriter
	waiters     map[*processWaitSlot]struct{}
}

type processWaitSlot struct {
	mu     sync.Mutex
	active bool
	done   chan struct{}
}

func newProcessWaitSlot() *processWaitSlot {
	return &processWaitSlot{active: true, done: make(chan struct{}, 1)}
}

func (s *processWaitSlot) deliver() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.active {
		return false
	}
	s.active = false
	s.done <- struct{}{}
	return true
}

func (s *processWaitSlot) cancel() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.active {
		return false
	}
	s.active = false
	return true
}

type ProcessTime struct {
	SecsSinceEpoch  int64 `json:"secs_since_epoch"`
	NanosSinceEpoch int32 `json:"nanos_since_epoch"`
}

type ProcessSnapshot struct {
	TaskID           string       `json:"task_id"`
	Command          string       `json:"command"`
	Description      string       `json:"description,omitempty"`
	CWD              string       `json:"cwd"`
	StartTime        ProcessTime  `json:"start_time"`
	EndTime          *ProcessTime `json:"end_time"`
	Output           string       `json:"output"`
	OutputFile       string       `json:"output_file"`
	Truncated        bool         `json:"truncated"`
	ExitCode         *int         `json:"exit_code"`
	Signal           *string      `json:"signal"`
	Completed        bool         `json:"completed"`
	Kind             string       `json:"kind"`
	BlockWaited      bool         `json:"block_waited"`
	ExplicitlyKilled bool         `json:"explicitly_killed"`
}

func NewProcessManager(ws *workspace.Workspace, approver Approver) *ProcessManager {
	return &ProcessManager{
		ws: ws, approver: approver, processes: make(map[string]*backgroundProcess),
		currentDir: ws.Root(), environment: os.Environ(),
	}
}

func (m *ProcessManager) ConfigureEnvironment(values map[string]string) {
	m.stateMu.Lock()
	m.environment = setEnvironment(os.Environ(), values)
	m.stateMu.Unlock()
}

func (m *ProcessManager) Start(ctx context.Context, command string) (string, error) {
	return m.start(ctx, command, "", 0, "bash")
}

func (m *ProcessManager) StartWithTimeout(ctx context.Context, command string, timeout time.Duration) (string, error) {
	return m.start(ctx, command, "", timeout, "bash")
}

func (m *ProcessManager) StartDescribed(ctx context.Context, command, description string, timeout time.Duration) (string, error) {
	return m.start(ctx, command, description, timeout, "bash")
}

func (m *ProcessManager) StartMonitor(ctx context.Context, command, description string, timeout time.Duration) (string, error) {
	return m.start(ctx, command, sanitizeMonitorDescription(description), timeout, "monitor")
}

func (m *ProcessManager) start(ctx context.Context, command, description string, timeout time.Duration, kind string) (string, error) {
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
	id := fmt.Sprintf("task_%d", m.nextID.Add(1))
	process := &backgroundProcess{
		id: id, command: command, cmd: cmd, output: buffer, started: time.Now(), done: make(chan struct{}),
		description: description, kind: kind,
	}
	var monitorReady chan struct{}
	if kind == "monitor" {
		monitorReady = make(chan struct{})
		process.monitor = newMonitorWriter(func(text string) {
			<-monitorReady
			if observer, ok := m.processObserver().(ProcessMonitorObserver); ok {
				observer.MonitorEvent(MonitorEvent{TaskID: id, Description: description, EventText: text})
			}
		}, func(message string) {
			<-monitorReady
			process.mu.Lock()
			if process.killed {
				process.mu.Unlock()
				return
			}
			process.killed = true
			process.mu.Unlock()
			if observer, ok := m.processObserver().(ProcessMonitorObserver); ok {
				observer.MonitorEvent(MonitorEvent{TaskID: id, Description: description, EventText: message})
			}
			go terminateProcess(cmd)
		})
		cmd.Stdout = io.MultiWriter(buffer, process.monitor)
	} else {
		cmd.Stdout = buffer
	}
	cmd.Stderr = buffer
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start command: %w", err)
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		if monitorReady != nil {
			close(monitorReady)
		}
		_ = terminateProcess(cmd)
		_ = cmd.Wait()
		return "", errors.New("process manager is closed")
	}
	m.processes[id] = process
	m.mu.Unlock()
	call, _ := ToolCallFromContext(ctx)
	if observer := m.processObserver(); observer != nil {
		observer.TaskBackgrounded(ProcessBackgrounded{
			ToolCallID: call.ID, TaskID: id, Command: command, CWD: cmd.Dir, Description: description, Kind: kind,
		})
	}
	if monitorReady != nil {
		close(monitorReady)
	}
	go func() {
		err := cmd.Wait()
		if process.monitor != nil {
			process.monitor.Close()
		}
		if checkpointErr := m.rewind.afterWorkspace(checkpoint); checkpointErr != nil {
			err = errors.Join(err, fmt.Errorf("checkpoint after background command: %w", checkpointErr))
		}
		process.mu.Lock()
		process.err = err
		process.ended = time.Now()
		process.mu.Unlock()
		process.deliverWaiters()
		if observer := m.processObserver(); observer != nil {
			observer.TaskCompleted(snapshotProcess(process, true))
		}
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

func (m *ProcessManager) SetObserver(observer ProcessObserver) {
	m.observerMu.Lock()
	m.observer = observer
	m.observerMu.Unlock()
}

func (m *ProcessManager) processObserver() ProcessObserver {
	m.observerMu.RLock()
	defer m.observerMu.RUnlock()
	return m.observer
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
		waiter := newProcessWaitSlot()
		process.mu.Lock()
		if process.terminal {
			process.mu.Unlock()
			<-process.done
			m.consume(process)
			return m.Output(id)
		}
		if process.waiters == nil {
			process.waiters = make(map[*processWaitSlot]struct{})
		}
		process.waiters[waiter] = struct{}{}
		process.mu.Unlock()
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		select {
		case <-waiter.done:
		case <-timer.C:
			if !waiter.cancel() {
				<-waiter.done
			} else {
				process.removeWaiter(waiter)
			}
		case <-ctx.Done():
			if !waiter.cancel() {
				<-waiter.done
			} else {
				process.removeWaiter(waiter)
				return "", ctx.Err()
			}
		}
	}
	process.mu.Lock()
	terminal := process.terminal
	process.mu.Unlock()
	if terminal {
		<-process.done
		m.consume(process)
	}
	return m.Output(id)
}

func (p *backgroundProcess) deliverWaiters() {
	p.mu.Lock()
	p.terminal = true
	waiters := make([]*processWaitSlot, 0, len(p.waiters))
	for waiter := range p.waiters {
		waiters = append(waiters, waiter)
	}
	p.waiters = nil
	p.mu.Unlock()
	delivered := false
	for _, waiter := range waiters {
		if waiter.deliver() {
			delivered = true
		}
	}
	if delivered {
		p.mu.Lock()
		p.blockWaited = true
		p.mu.Unlock()
	}
}

func (p *backgroundProcess) removeWaiter(waiter *processWaitSlot) {
	p.mu.Lock()
	delete(p.waiters, waiter)
	p.mu.Unlock()
}

func (m *ProcessManager) consume(process *backgroundProcess) {
	process.mu.Lock()
	consume := !process.consumed
	process.consumed = true
	process.mu.Unlock()
	if consume {
		if observer, ok := m.processObserver().(ProcessConsumptionObserver); ok {
			observer.TaskConsumed(process.id)
		}
	}
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
	process.mu.Lock()
	process.killed = true
	process.mu.Unlock()
	if err := terminateProcess(process.cmd); err != nil {
		process.mu.Lock()
		process.killed = false
		process.mu.Unlock()
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

func (m *ProcessManager) Snapshots() []ProcessSnapshot {
	m.mu.Lock()
	processes := make([]*backgroundProcess, 0, len(m.processes))
	for _, process := range m.processes {
		processes = append(processes, process)
	}
	m.mu.Unlock()
	result := make([]ProcessSnapshot, 0, len(processes))
	for _, process := range processes {
		completed := false
		select {
		case <-process.done:
			completed = true
		default:
		}
		result = append(result, snapshotProcess(process, completed))
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].StartTime == result[j].StartTime {
			return result[i].TaskID < result[j].TaskID
		}
		if result[i].StartTime.SecsSinceEpoch == result[j].StartTime.SecsSinceEpoch {
			return result[i].StartTime.NanosSinceEpoch < result[j].StartTime.NanosSinceEpoch
		}
		return result[i].StartTime.SecsSinceEpoch < result[j].StartTime.SecsSinceEpoch
	})
	return result
}

func snapshotProcess(process *backgroundProcess, completed bool) ProcessSnapshot {
	process.mu.Lock()
	ended, killed, blockWaited := process.ended, process.killed, process.blockWaited
	process.mu.Unlock()
	output, truncated := process.output.Snapshot()
	item := ProcessSnapshot{
		TaskID: process.id, Command: process.command, Description: process.description, CWD: process.cmd.Dir,
		StartTime: processTime(process.started), Output: output, Truncated: truncated,
		Completed: completed, Kind: process.kind, BlockWaited: blockWaited, ExplicitlyKilled: killed,
	}
	if completed {
		end := processTime(ended)
		item.EndTime = &end
		if process.cmd.ProcessState != nil && process.cmd.ProcessState.ExitCode() >= 0 {
			code := process.cmd.ProcessState.ExitCode()
			item.ExitCode = &code
		}
	}
	return item
}

func processTime(value time.Time) ProcessTime {
	return ProcessTime{SecsSinceEpoch: value.Unix(), NanosSinceEpoch: int32(value.Nanosecond())}
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

type monitorWriter struct {
	mu               sync.Mutex
	partial          []byte
	pending          []string
	timer            *time.Timer
	emit             func(string)
	autoKill         func(string)
	tokens           int
	lastRefill       time.Time
	suppressed       int
	suppressionStart time.Time
	lastSuppression  time.Time
	killed           bool
	closed           bool
}

func newMonitorWriter(emit, autoKill func(string)) *monitorWriter {
	return &monitorWriter{emit: emit, autoKill: autoKill, tokens: monitorRateCapacity, lastRefill: time.Now()}
}

func (w *monitorWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return len(data), nil
	}
	w.partial = append(w.partial, data...)
	if len(w.partial) > backgroundOutputBytes {
		w.partial = append([]byte(nil), w.partial[len(w.partial)-backgroundOutputBytes:]...)
	}
	for {
		index := bytes.IndexByte(w.partial, '\n')
		if index < 0 {
			break
		}
		w.addLineLocked(w.partial[:index])
		w.partial = w.partial[index+1:]
	}
	w.mu.Unlock()
	return len(data), nil
}

func (w *monitorWriter) addLineLocked(raw []byte) {
	line := strings.TrimSpace(string(raw))
	if line == "" {
		return
	}
	w.pending = append(w.pending, truncateMonitorText(line, monitorLineBytes, "...(truncated)"))
	if w.timer != nil {
		w.timer.Stop()
	}
	w.timer = time.AfterFunc(monitorDebounce, w.flushBatch)
}

func (w *monitorWriter) flushBatch() {
	w.mu.Lock()
	if len(w.pending) == 0 || w.closed || w.killed {
		w.mu.Unlock()
		return
	}
	text := truncateMonitorText(strings.Join(w.pending, "\n"), monitorBatchBytes, "\n...(truncated)")
	w.pending = nil
	now := time.Now()
	refills := int(now.Sub(w.lastRefill) / monitorRateRefill)
	if refills > 0 {
		w.tokens = min(monitorRateCapacity, w.tokens+refills)
		w.lastRefill = w.lastRefill.Add(time.Duration(refills) * monitorRateRefill)
	}
	if w.tokens > 0 {
		w.tokens--
		suppressed := w.suppressed
		w.suppressed = 0
		if !w.lastSuppression.IsZero() && now.Sub(w.lastSuppression) > 3*monitorRateRefill {
			w.suppressionStart = time.Time{}
		}
		emit := w.emit
		w.mu.Unlock()
		if suppressed > 0 {
			emit(fmt.Sprintf("[%d events suppressed -- output rate too high. Consider restarting this monitor with a more selective filter.]", suppressed))
		}
		emit(text)
		return
	}
	w.suppressed++
	w.lastSuppression = now
	if w.suppressionStart.IsZero() {
		w.suppressionStart = now
	}
	overloaded := now.Sub(w.suppressionStart) > monitorOverloadLimit
	suppressed := w.suppressed
	autoKill := w.autoKill
	if overloaded {
		w.killed = true
	}
	w.mu.Unlock()
	if overloaded {
		autoKill(fmt.Sprintf("[Monitor stopped -- output rate remained too high (%d events suppressed over 30s). Restart it with a more selective filter.]", suppressed))
	}
}

func (w *monitorWriter) Close() {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	if w.timer != nil {
		w.timer.Stop()
	}
	if line := strings.TrimSpace(string(w.partial)); line != "" {
		w.pending = append(w.pending, truncateMonitorText(line, monitorLineBytes, "...(truncated)"))
	}
	w.partial = nil
	w.mu.Unlock()
	w.flushBatch()
	w.mu.Lock()
	w.closed = true
	w.mu.Unlock()
}

func truncateMonitorText(value string, limit int, suffix string) string {
	if len(value) <= limit {
		return value
	}
	end := limit
	for end > 0 && !utf8.RuneStart(value[end]) {
		end--
	}
	return value[:end] + suffix
}

func sanitizeMonitorDescription(description string) string {
	description = strings.ReplaceAll(description, "\"", "'")
	description = strings.NewReplacer("\n", " ", "\r", " ").Replace(description)
	return description
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
	value, _ := b.Snapshot()
	return value
}

func (b *tailBuffer) Snapshot() (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.truncated {
		return "[earlier output truncated]\n" + string(b.data), true
	}
	return string(b.data), false
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

type monitorTool struct{ manager *ProcessManager }

func (t *monitorTool) Definition() api.ToolDefinition {
	return api.ToolDefinition{
		Type: "function", Name: "monitor",
		Description: "Run a background monitor. Each stdout line is delivered as a real-time event; do not poll or sleep while waiting.",
		Parameters: objectSchema(map[string]any{
			"command":     map[string]any{"type": "string", "description": "Shell command whose stdout lines are events."},
			"description": map[string]any{"type": "string", "description": "Short description shown with each event."},
			"timeout_ms":  map[string]any{"type": "integer", "minimum": 0},
			"persistent":  map[string]any{"type": "boolean"},
		}, "command", "description"),
	}
}

func (t *monitorTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		Command     string  `json:"command"`
		Description string  `json:"description"`
		TimeoutMS   *uint64 `json:"timeout_ms"`
		Persistent  bool    `json:"persistent"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("decode monitor arguments: %w", err)
	}
	const maxTimeout = uint64(36_000_000)
	if args.TimeoutMS != nil && *args.TimeoutMS > maxTimeout && !args.Persistent {
		return "", fmt.Errorf("persistent must be true when timeout_ms exceeds %dms", maxTimeout)
	}
	timeoutMS := maxTimeout
	if args.TimeoutMS != nil {
		timeoutMS = *args.TimeoutMS
	}
	if args.Persistent {
		timeoutMS = 0
	}
	id, err := t.manager.StartMonitor(ctx, args.Command, args.Description, time.Duration(timeoutMS)*time.Millisecond)
	if err != nil {
		return "", err
	}
	deadline := fmt.Sprintf("timeout %dms", timeoutMS)
	if timeoutMS == 0 {
		deadline = "persistent -- runs until kill_task or session end"
	}
	return fmt.Sprintf("Monitor started (task %s, %s).\nYou will be notified on each event. Keep working -- do not poll or sleep.", id, deadline), nil
}

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
		id, err := t.manager.StartDescribed(ctx, args.Command, args.Description, timeout)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("Background task %s started\nUse get_task_output with task_ids=[%q] to retrieve the output.", id, id), nil
	}
	return t.manager.RunForeground(ctx, args.Command, timeout)
}

type taskOutputTool struct {
	manager   *ProcessManager
	subagents *subagentHolder
}

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
		var output string
		var err error
		var backend SubagentBackend
		if t.subagents != nil {
			backend = t.subagents.get()
		}
		if backend != nil && backend.Has(id) {
			var result SubagentResult
			result, err = backend.Output(ctx, id, remaining)
			if err == nil {
				encoded, _ := json.Marshal(result)
				output = string(encoded)
			}
		} else {
			output, err = t.manager.WaitOutput(ctx, id, remaining)
		}
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

type killTaskTool struct {
	manager   *ProcessManager
	subagents *subagentHolder
}

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
	var backend SubagentBackend
	if t.subagents != nil {
		backend = t.subagents.get()
	}
	if backend != nil && backend.Has(args.TaskID) {
		outcome, err := backend.Kill(ctx, args.TaskID)
		if err != nil {
			return "", err
		}
		if outcome == "already_finished" {
			return fmt.Sprintf("task_id: %s\noutcome: already_exited\nmessage: Subagent had already completed", args.TaskID), nil
		}
		return fmt.Sprintf("task_id: %s\noutcome: killed\nmessage: Subagent was terminated successfully", args.TaskID), nil
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
