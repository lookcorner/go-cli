//go:build !windows

package notify

import (
	"os/exec"
	"syscall"
)

// detachProcessGroup puts a hook in its own session so a timeout can kill the
// whole tree instead of leaking orphaned children.
func detachProcessGroup(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

// killProcessGroup terminates a detached hook and everything it started.
func killProcessGroup(pid int) {
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}
