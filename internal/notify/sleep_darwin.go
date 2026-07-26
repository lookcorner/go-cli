//go:build darwin

package notify

import (
	"os"
	"strconv"
)

// sleepCommand holds a macOS idle-sleep assertion for as long as it runs. It
// also waits on this process so a crash cannot leave the machine awake.
func sleepCommand() []string {
	return []string{"caffeinate", "-i", "-w", strconv.Itoa(os.Getpid())}
}
