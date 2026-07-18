//go:build !darwin && !linux

package worktree

import "os"

func cloneFile(source, dest string, mode os.FileMode) error {
	return copyFile(source, dest, mode)
}
