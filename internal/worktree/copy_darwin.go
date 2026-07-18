//go:build darwin

package worktree

import (
	"os"

	"golang.org/x/sys/unix"
)

func cloneFile(source, dest string, mode os.FileMode) error {
	if err := unix.Clonefile(source, dest, 0); err != nil {
		return copyFile(source, dest, mode)
	}
	return os.Chmod(dest, mode)
}
