//go:build linux

package worktree

import (
	"os"

	"golang.org/x/sys/unix"
)

func cloneFile(source, dest string, mode os.FileMode) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	cloneErr := unix.IoctlFileClone(int(out.Fd()), int(in.Fd()))
	closeErr := out.Close()
	if cloneErr == nil && closeErr == nil {
		return os.Chmod(dest, mode)
	}
	_ = os.Remove(dest)
	return copyFile(source, dest, mode)
}
