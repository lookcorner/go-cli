//go:build windows

package tools

import "os/exec"

func configureProcessGroup(_ *exec.Cmd) {}

func terminateProcess(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}

func forceKillProcess(cmd *exec.Cmd) error { return terminateProcess(cmd) }
