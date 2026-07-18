//go:build !windows

package acp

import (
	"errors"
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
