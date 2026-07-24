//go:build linux

package leader

import (
	"errors"
	"os"
	"strconv"
	"syscall"
)

func isGorkProcess(pid uint32) bool {
	path, err := os.Readlink("/proc/" + strconv.FormatUint(uint64(pid), 10) + "/exe")
	if err != nil {
		return false
	}
	return isGorkExecutable(path)
}

func terminateProcess(pid uint32) error {
	err := syscall.Kill(int(pid), syscall.SIGTERM)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}
