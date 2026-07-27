//go:build !linux

package tools

import "io"

func ensureParentBwrapHookWriteDeny(_, _, _ string, _ []string, _ io.Writer) error {
	// Non-Linux: slots ensured by EnsureParentBwrapHookWriteDeny; Seatbelt/Landlock
	// handle OS confinement elsewhere. No parent bwrap re-exec.
	return nil
}
