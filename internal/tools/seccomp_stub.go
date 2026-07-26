//go:build !linux

package tools

// MaybeExecSeccompNamespaceLockdown is a no-op outside Linux.
func MaybeExecSeccompNamespaceLockdown([]string) bool { return false }

func wrapBubblewrapWithSeccomp(bwrapArgs []string) []string { return bwrapArgs }
