package terminaldiag

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

const tmuxProbeTimeout = 500 * time.Millisecond

type tmuxProbe struct {
	SetClipboard     string
	AllowPassthrough string
	ExtendedKeys     string
	ControlMode      string // "on", "off", or "" when unavailable
}

// probeTmuxOption returns the live tmux option value when available.
var probeTmuxOption = liveTmuxOption

// probeTmuxControlMode reports whether the attached client is in control mode.
var probeTmuxControlMode = liveTmuxControlMode

func liveTmuxOption(option string, window bool) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), tmuxProbeTimeout)
	defer cancel()
	args := []string{"show-options", "-gv", option}
	if window {
		args = []string{"show-options", "-gwv", option}
	}
	cmd := exec.CommandContext(ctx, "tmux", args...)
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	value := strings.TrimSpace(string(out))
	if value == "" {
		return "", false
	}
	// tmux may print "set-clipboard on" or just "on" depending on flags/version.
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return "", false
	}
	return fields[len(fields)-1], true
}

func liveTmuxControlMode() (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), tmuxProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "tmux", "display-message", "-p", "#{client_flags}")
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	if strings.Contains(string(out), "control-mode") {
		return "on", true
	}
	return "off", true
}

func collectTmuxProbe(getenv func(string) string) tmuxProbe {
	if getenv("TMUX") == "" {
		return tmuxProbe{}
	}
	var probe tmuxProbe
	if value, ok := probeTmuxOption("set-clipboard", false); ok {
		probe.SetClipboard = value
	}
	if value, ok := probeTmuxOption("allow-passthrough", true); ok {
		probe.AllowPassthrough = value
	}
	if value, ok := probeTmuxOption("extended-keys", false); ok {
		probe.ExtendedKeys = value
	}
	if value, ok := probeTmuxControlMode(); ok {
		probe.ControlMode = value
	}
	return probe
}

func tmuxProbeFindings(probe tmuxProbe) []string {
	var findings []string
	if probe.ControlMode == "on" {
		findings = append(findings, "Display may be limited in tmux control mode.\n    If display problems continue, connect with a regular tmux client instead of control mode.")
	}
	if probe.SetClipboard != "" && !containsString([]string{"on", "external"}, probe.SetClipboard) {
		findings = append(findings, "tmux set-clipboard is "+probe.SetClipboard+"; OSC 52 clipboard may not forward.\n    → Automatic setup: `gork doctor fix tmux-clipboard`")
	}
	if probe.AllowPassthrough != "" && !containsString([]string{"on", "all"}, probe.AllowPassthrough) {
		findings = append(findings, "tmux allow-passthrough is "+probe.AllowPassthrough+"; DCS image/clipboard passthrough may fail.\n    → Automatic setup: `gork doctor fix dcs-passthrough`")
	}
	if probe.ExtendedKeys == "off" {
		findings = append(findings, "tmux extended-keys is off; some modified key chords may not reach the TUI.\n    → Automatic setup: `gork doctor fix tmux-extended-keys`")
	}
	return findings
}
