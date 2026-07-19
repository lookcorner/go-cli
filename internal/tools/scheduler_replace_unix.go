//go:build !windows

package tools

import "os"

func replaceStateFile(source, target string) error {
	return os.Rename(source, target)
}
