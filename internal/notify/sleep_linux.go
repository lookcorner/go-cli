//go:build linux

package notify

// sleepCommand blocks logind idle handling for as long as it runs.
func sleepCommand() []string {
	return []string{"systemd-inhibit", "--what=idle", "--who=grok", "--why=agent turn in progress", "sleep", "infinity"}
}
