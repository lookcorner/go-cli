//go:build !linux

package tools

import "io"

func applyParentLandlock(SandboxProfile, string, io.Writer) error { return nil }
