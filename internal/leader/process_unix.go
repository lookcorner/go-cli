//go:build unix && !linux

package leader

import (
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

func isGorkProcess(pid uint32) bool {
	output, err := exec.Command("ps", "-p", strconv.FormatUint(uint64(pid), 10), "-o", "comm=").Output()
	if err != nil {
		return false
	}
	return isGorkExecutable(strings.TrimSpace(string(output)))
}

func terminateProcess(pid uint32) error {
	err := syscall.Kill(int(pid), syscall.SIGTERM)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}
