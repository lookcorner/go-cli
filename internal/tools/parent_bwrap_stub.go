//go:build !linux

package tools

import "io"

func ensureParentBwrap(_, _, _ string, needsHooks, _ bool, _ []string, _ io.Writer) error {
	// Non-Linux: hook slots ensured by EnsureParentBwrap when needed; Seatbelt
	// handles OS confinement elsewhere. Deny bind-over is Linux-only.
	_ = needsHooks
	return nil
}
