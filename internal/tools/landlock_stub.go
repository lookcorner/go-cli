//go:build !linux

package tools

import "io"

func applyParentLandlockResolved(ResolvedSandboxProfile, string, io.Writer) (bool, error) {
	return false, nil
}
