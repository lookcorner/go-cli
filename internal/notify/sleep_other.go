//go:build !darwin && !linux

package notify

// sleepCommand reports that this platform has no idle-sleep inhibitor.
func sleepCommand() []string { return nil }
