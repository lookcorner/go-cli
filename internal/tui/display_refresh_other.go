//go:build !darwin && !windows

package tui

func probeDisplayRefreshHz() (uint32, bool) { return 0, false }
