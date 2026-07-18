package acp

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const terminalOutputBytes = 256 << 10
const terminalActivityInterval = 500 * time.Millisecond

type terminalManager struct {
	mu        sync.Mutex
	terminals map[string]*ptyTerminal
	notify    func(string, any)
}

type ptyTerminal struct {
	id      string
	file    *os.File
	fd      int
	cmd     *exec.Cmd
	cwd     string
	name    string
	created int64
	done    chan struct{}

	mu           sync.Mutex
	rows         uint16
	cols         uint16
	output       []byte
	outputOffset uint64
	exited       bool
	exitCode     *int
	busy         bool
}

type terminalInfo struct {
	TerminalID   string `json:"terminalId"`
	Status       string `json:"status"`
	Interactive  bool   `json:"interactive"`
	Name         string `json:"name,omitempty"`
	ExitCode     *int   `json:"exitCode"`
	CWD          string `json:"cwd,omitempty"`
	OutputOffset uint64 `json:"outputOffset"`
	CreatedAt    int64  `json:"createdAt"`
}

func newTerminalManager(notify func(string, any)) *terminalManager {
	return &terminalManager{terminals: make(map[string]*ptyTerminal), notify: notify}
}

func (m *terminalManager) create(shell, cwd string, env map[string]string, rows, cols uint16, name string) (string, error) {
	if rows == 0 || cols == 0 {
		return "", errors.New("terminal rows and cols must be positive")
	}
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	info, err := os.Stat(cwd)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("terminal cwd is not a directory: %s", cwd)
	}
	explicitShell := shell != ""
	if !explicitShell {
		shell = os.Getenv("SHELL")
		if shell == "" {
			if runtime.GOOS == "windows" {
				shell = "cmd.exe"
			} else {
				shell = "/bin/sh"
			}
		}
	}
	args := []string(nil)
	if runtime.GOOS != "windows" && !explicitShell {
		args = []string{"-l"}
	}
	cmd := exec.Command(shell, args...)
	cmd.Dir = cwd
	cmd.Env = terminalEnvironment(env)
	file, err := startTerminal(cmd, rows, cols)
	if err != nil {
		return "", fmt.Errorf("start PTY shell: %w", err)
	}
	idBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		_ = killTerminalProcess(cmd)
		_ = file.Close()
		_ = cmd.Wait()
		return "", fmt.Errorf("create terminal ID: %w", err)
	}
	id := hex.EncodeToString(idBytes)
	if name == "" {
		name = filepath.Base(cwd)
		if name == "." || name == string(filepath.Separator) || name == "" {
			name = filepath.Base(shell)
		}
	}
	terminal := &ptyTerminal{
		id: id, file: file, fd: int(file.Fd()), cmd: cmd, cwd: cwd, name: name, created: time.Now().Unix(),
		rows: rows, cols: cols, done: make(chan struct{}),
	}
	m.mu.Lock()
	m.terminals[id] = terminal
	m.mu.Unlock()
	go m.readOutput(terminal)
	go m.monitorActivity(terminal)
	return id, nil
}

func terminalEnvironment(extra map[string]string) []string {
	values := map[string]string{"TERM": "xterm-256color", "COLORTERM": "truecolor", "LANG": "en_US.UTF-8", "LC_ALL": "en_US.UTF-8"}
	for key, value := range extra {
		values[key] = value
	}
	result := append([]string(nil), os.Environ()...)
	indexes := make(map[string]int, len(result))
	for index, entry := range result {
		if key, _, ok := strings.Cut(entry, "="); ok {
			indexes[key] = index
		}
	}
	for key, value := range values {
		entry := key + "=" + value
		if index, ok := indexes[key]; ok {
			result[index] = entry
		} else {
			result = append(result, entry)
		}
	}
	return result
}

func (m *terminalManager) readOutput(terminal *ptyTerminal) {
	buffer := make([]byte, 4096)
	for {
		count, err := terminal.file.Read(buffer)
		if count > 0 {
			data := append([]byte(nil), buffer[:count]...)
			offset := terminal.recordOutput(data)
			m.send("x.ai/terminal/pty/notification", map[string]any{
				"terminalId": terminal.id, "type": "output",
				"data": base64.StdEncoding.EncodeToString(data), "outputOffset": offset,
			})
		}
		if err != nil {
			break
		}
	}
	_ = terminal.cmd.Wait()
	code := -1
	if terminal.cmd.ProcessState != nil {
		code = terminal.cmd.ProcessState.ExitCode()
	}
	terminal.mu.Lock()
	wasBusy := terminal.busy
	terminal.busy = false
	terminal.exited = true
	terminal.exitCode = &code
	// Keep file descriptor inspection in sampleActivity serialized with close.
	_ = terminal.file.Close()
	terminal.mu.Unlock()
	if wasBusy {
		m.sendActivity(terminal.id, false)
	}
	close(terminal.done)
	m.send("x.ai/terminal/pty/notification", map[string]any{
		"terminalId": terminal.id, "type": "exit", "exitCode": code,
	})
}

func (m *terminalManager) monitorActivity(terminal *ptyTerminal) {
	ticker := time.NewTicker(terminalActivityInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			m.sampleActivity(terminal, false)
		case <-terminal.done:
			return
		}
	}
}

func (m *terminalManager) sampleActivity(terminal *ptyTerminal, reportCurrent bool) {
	terminal.mu.Lock()
	if terminal.exited {
		terminal.mu.Unlock()
		return
	}
	busy := terminalHasForegroundProcess(terminal.fd, terminal.cmd.Process.Pid)
	changed := busy != terminal.busy
	terminal.busy = busy
	terminal.mu.Unlock()
	if changed || reportCurrent {
		m.sendActivity(terminal.id, busy)
	}
}

func (m *terminalManager) sendActivity(id string, busy bool) {
	typeName := "process_ended"
	if busy {
		typeName = "process_started"
	}
	m.send("x.ai/terminal/pty/notification", map[string]any{"terminalId": id, "type": typeName})
}

func (t *ptyTerminal) recordOutput(data []byte) uint64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.output = append(t.output, data...)
	if len(t.output) > terminalOutputBytes {
		t.output = append([]byte(nil), t.output[len(t.output)-terminalOutputBytes:]...)
	}
	t.outputOffset += uint64(len(data))
	return t.outputOffset
}

func (m *terminalManager) send(method string, params any) {
	if m.notify != nil {
		m.notify(method, params)
	}
}

func (m *terminalManager) lookup(id string) (*ptyTerminal, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	terminal := m.terminals[id]
	if terminal == nil {
		return nil, fmt.Errorf("terminal %q not found", id)
	}
	return terminal, nil
}

func (m *terminalManager) writeInput(id string, data []byte) error {
	terminal, err := m.lookup(id)
	if err != nil {
		return err
	}
	terminal.mu.Lock()
	if terminal.exited {
		terminal.mu.Unlock()
		return fmt.Errorf("terminal %q exited", id)
	}
	if _, err = terminal.file.Write(data); err != nil {
		terminal.mu.Unlock()
		return err
	}
	// Release the lock before sampling, which acquires it itself.
	terminal.mu.Unlock()
	m.sampleActivity(terminal, false)
	return nil
}

func (m *terminalManager) resize(id string, rows, cols uint16) error {
	if rows == 0 || cols == 0 {
		return errors.New("terminal rows and cols must be positive")
	}
	terminal, err := m.lookup(id)
	if err != nil {
		return err
	}
	terminal.mu.Lock()
	defer terminal.mu.Unlock()
	if terminal.exited {
		return fmt.Errorf("terminal %q exited", id)
	}
	if err := resizeTerminal(terminal.file, rows, cols); err != nil {
		return err
	}
	terminal.rows, terminal.cols = rows, cols
	return nil
}

func (m *terminalManager) load(id string) (map[string]any, error) {
	terminal, err := m.lookup(id)
	if err != nil {
		return nil, err
	}
	terminal.mu.Lock()
	replay := append([]byte(nil), terminal.output...)
	offset, rows, cols := terminal.outputOffset, terminal.rows, terminal.cols
	exited, exitCode := terminal.exited, terminal.exitCode
	terminal.mu.Unlock()
	if len(replay) > 0 {
		m.send("x.ai/terminal/pty/notification", map[string]any{
			"terminalId": id, "type": "output", "data": base64.StdEncoding.EncodeToString(replay),
			"outputOffset": offset, "isReplay": true,
		})
	}
	if exited {
		m.send("x.ai/terminal/pty/notification", map[string]any{
			"terminalId": id, "type": "exit", "exitCode": exitCode, "isReplay": true,
		})
	} else {
		m.sampleActivity(terminal, true)
	}
	result := map[string]any{"terminalId": id, "rows": rows, "cols": cols, "exited": exited}
	if exitCode != nil {
		result["exitCode"] = *exitCode
	}
	return result, nil
}

func (m *terminalManager) list() []terminalInfo {
	m.mu.Lock()
	terminals := make([]*ptyTerminal, 0, len(m.terminals))
	for _, terminal := range m.terminals {
		terminals = append(terminals, terminal)
	}
	m.mu.Unlock()
	result := make([]terminalInfo, 0, len(terminals))
	for _, terminal := range terminals {
		terminal.mu.Lock()
		status := "connected"
		if terminal.exited {
			status = "exited"
		}
		result = append(result, terminalInfo{
			TerminalID: terminal.id, Status: status, Interactive: true, Name: terminal.name,
			ExitCode: terminal.exitCode, CWD: terminal.cwd, OutputOffset: terminal.outputOffset, CreatedAt: terminal.created,
		})
		terminal.mu.Unlock()
	}
	return result
}

func (m *terminalManager) kill(id string) (string, error) {
	terminal, err := m.lookup(id)
	if err != nil {
		return "", err
	}
	terminal.mu.Lock()
	exited := terminal.exited
	terminal.mu.Unlock()
	outcome := "already_exited"
	if !exited {
		outcome = "killed"
		_ = killTerminalProcess(terminal.cmd)
		terminal.mu.Lock()
		_ = terminal.file.Close()
		terminal.mu.Unlock()
		select {
		case <-terminal.done:
		case <-time.After(3 * time.Second):
			return "", fmt.Errorf("terminal %q did not exit", id)
		}
	}
	m.mu.Lock()
	delete(m.terminals, id)
	m.mu.Unlock()
	return outcome, nil
}

func (m *terminalManager) closeAll() {
	m.mu.Lock()
	terminals := make([]*ptyTerminal, 0, len(m.terminals))
	for _, terminal := range m.terminals {
		terminals = append(terminals, terminal)
	}
	m.terminals = make(map[string]*ptyTerminal)
	m.mu.Unlock()
	for _, terminal := range terminals {
		terminal.mu.Lock()
		exited := terminal.exited
		terminal.mu.Unlock()
		if !exited {
			_ = killTerminalProcess(terminal.cmd)
			terminal.mu.Lock()
			_ = terminal.file.Close()
			terminal.mu.Unlock()
			select {
			case <-terminal.done:
			case <-time.After(3 * time.Second):
			}
		}
	}
}
