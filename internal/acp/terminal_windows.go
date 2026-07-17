//go:build windows

package acp

import (
	"errors"
	"os"
	"os/exec"
)

func startTerminal(_ *exec.Cmd, _, _ uint16) (*os.File, error) {
	return nil, errors.New("interactive PTY terminals are not supported on Windows")
}

func resizeTerminal(_ *os.File, _, _ uint16) error {
	return errors.New("interactive PTY terminals are not supported on Windows")
}

func terminalHasForegroundProcess(_ *os.File, _ int) bool { return false }

func killTerminalProcess(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
