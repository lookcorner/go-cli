//go:build !windows

package workspace

import "os"

func atomicReplace(source, target string) error { return os.Rename(source, target) }
