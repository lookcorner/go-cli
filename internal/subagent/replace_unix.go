//go:build !windows

package subagent

import "os"

func replaceFile(source, target string) error { return os.Rename(source, target) }
