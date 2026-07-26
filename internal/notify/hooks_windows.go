//go:build windows

package notify

import (
	"os"
	"os/exec"
)

// detachProcessGroup has no Windows equivalent; hooks run as plain children.
func detachProcessGroup(*exec.Cmd) {}

// killProcessGroup terminates a timed-out hook process.
func killProcessGroup(pid int) {
	if process, err := os.FindProcess(pid); err == nil {
		_ = process.Kill()
	}
}
