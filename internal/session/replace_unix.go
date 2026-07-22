//go:build !windows

package session

import "os"

func atomicReplace(source, target string) error { return os.Rename(source, target) }
