//go:build !windows

package acp

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

func startTerminal(cmd *exec.Cmd, rows, cols uint16) (*os.File, error) {
	return pty.StartWithSize(cmd, &pty.Winsize{Rows: rows, Cols: cols})
}

func resizeTerminal(file *os.File, rows, cols uint16) error {
	return pty.Setsize(file, &pty.Winsize{Rows: rows, Cols: cols})
}

func terminalHasForegroundProcess(fd, shellPID int) bool {
	foreground, err := unix.IoctlGetInt(fd, unix.TIOCGPGRP)
	return err == nil && foreground > 0 && foreground != shellPID
}

func configureTerminalProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func terminalProcessStatus(state *os.ProcessState) (*int, string) {
	if state == nil {
		return nil, ""
	}
	if status, ok := state.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		return nil, fmt.Sprintf("signal %d", status.Signal())
	}
	code := state.ExitCode()
	return &code, ""
}

func killTerminalProcess(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}
