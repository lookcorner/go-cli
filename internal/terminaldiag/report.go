package terminaldiag

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

const SchemaVersion = "1"

type Snapshot struct {
	SchemaVersion string   `json:"schemaVersion"`
	Facts         Facts    `json:"facts"`
	Findings      []string `json:"findings"`
	Counts        Counts   `json:"counts"`
}

type Facts struct {
	Terminal         string `json:"terminal"`
	Multiplexer      string `json:"multiplexer"`
	SSH              bool   `json:"ssh"`
	Color            string `json:"color"`
	NativeClip       bool   `json:"nativeClipboard"`
	ClipboardTool    string `json:"clipboardTool,omitempty"`
	OSC52            bool   `json:"osc52"`
	GOOS             string `json:"goos"`
	SetClipboard     string `json:"tmuxSetClipboard,omitempty"`
	AllowPassthrough string `json:"tmuxAllowPassthrough,omitempty"`
	ExtendedKeys     string `json:"tmuxExtendedKeys,omitempty"`
}

type Counts struct {
	Issues int `json:"issues"`
}

func IsCommand(prompt string) bool {
	fields := strings.Fields(prompt)
	if len(fields) == 0 {
		return false
	}
	switch fields[0] {
	case "/doctor", "/terminal-setup", "/terminal-check", "/terminal-info":
		return true
	default:
		return false
	}
}

func Report() string {
	return BuildSnapshot(os.Getenv, exec.LookPath, runtime.GOOS).Human()
}

func ReportJSON() ([]byte, error) {
	return json.MarshalIndent(BuildSnapshot(os.Getenv, exec.LookPath, runtime.GOOS), "", "  ")
}

func SSHWrapRecommended() bool {
	return sshWrapRecommended(os.Getenv)
}

func sshWrapRecommended(getenv func(string) string) bool {
	ssh := getenv("SSH_CONNECTION") != "" || getenv("SSH_TTY") != ""
	return sshWrapRecommendation(getenv, ssh) != ""
}

func BuildSnapshot(getenv func(string) string, lookPath func(string) (string, error), goos string) Snapshot {
	term := strings.TrimSpace(getenv("TERM"))
	brand := terminalBrand(getenv, term)
	multiplexer := terminalMultiplexer(getenv)
	ssh := getenv("SSH_CONNECTION") != "" || getenv("SSH_TTY") != ""
	color := colorSupport(getenv, term)
	clipboard, clipboardTool := nativeClipboard(lookPath, goos)
	osc52 := term != "dumb" && (term != "" || brand != "unknown")
	findings := terminalWarnings(term, color, multiplexer, clipboard, osc52)
	var tmux tmuxProbe
	if strings.Contains(multiplexer, "byobu (screen)") {
		findings = append(findings, "Byobu is using GNU screen, which has limited clipboard and display support.\n    Switch Byobu to its tmux backend, then restart or reattach the session.")
	} else {
		tmux = collectTmuxProbe(getenv)
		findings = append(findings, tmuxProbeFindings(tmux)...)
	}
	if finding := sshWrapRecommendation(getenv, ssh); finding != "" {
		findings = append(findings, finding)
	}
	return Snapshot{
		SchemaVersion: SchemaVersion,
		Facts: Facts{
			Terminal: brand, Multiplexer: multiplexer, SSH: ssh, Color: color,
			NativeClip: clipboard, ClipboardTool: clipboardTool, OSC52: osc52, GOOS: goos,
			SetClipboard: tmux.SetClipboard, AllowPassthrough: tmux.AllowPassthrough, ExtendedKeys: tmux.ExtendedKeys,
		},
		Findings: findings,
		Counts:   Counts{Issues: len(findings)},
	}
}

func (s Snapshot) Human() string {
	var out strings.Builder
	fmt.Fprintf(&out, "Environment\n  terminal     %s\n  multiplexer  %s\n  ssh          %s\n  color        %s\n",
		s.Facts.Terminal, s.Facts.Multiplexer, yesNo(s.Facts.SSH), s.Facts.Color)
	if s.Facts.Multiplexer == "tmux" || strings.Contains(s.Facts.Multiplexer, "tmux") {
		fmt.Fprintf(&out, "  set-clipboard %s\n  allow-passthrough %s\n  extended-keys %s\n",
			tmuxFactOrUnknown(s.Facts.SetClipboard), tmuxFactOrUnknown(s.Facts.AllowPassthrough), tmuxFactOrUnknown(s.Facts.ExtendedKeys))
	}
	fmt.Fprintf(&out, "\nClipboard routes\n  native       %s", activeOff(s.Facts.NativeClip))
	if s.Facts.ClipboardTool != "" {
		fmt.Fprintf(&out, " (tool: %s)", s.Facts.ClipboardTool)
	}
	fmt.Fprintf(&out, "\n  osc 52       %s\n", activeOff(s.Facts.OSC52))
	if len(s.Findings) == 0 {
		out.WriteString("\nNo issues found.")
		return out.String()
	}
	fmt.Fprintf(&out, "\n%d issue(s)\n", len(s.Findings))
	for _, warning := range s.Findings {
		fmt.Fprintf(&out, "\n  [!] %s", warning)
	}
	return out.String()
}

func tmuxFactOrUnknown(value string) string {
	if value == "" {
		return "unknown"
	}
	return value
}

func buildReport(getenv func(string) string, lookPath func(string) (string, error), goos string) string {
	return BuildSnapshot(getenv, lookPath, goos).Human()
}

func terminalBrand(getenv func(string) string, term string) string {
	if value := strings.TrimSpace(getenv("TERM_PROGRAM")); value != "" {
		return value
	}
	if getenv("WT_SESSION") != "" {
		return "Windows Terminal"
	}
	if getenv("KITTY_WINDOW_ID") != "" {
		return "Kitty"
	}
	if term != "" {
		return term
	}
	return "unknown"
}

func terminalMultiplexer(getenv func(string) string) string {
	backend := strings.ToLower(strings.TrimSpace(getenv("BYOBU_BACKEND")))
	if getenv("TMUX") != "" {
		if backend != "" {
			return "byobu (" + backend + ")"
		}
		return "tmux"
	}
	if getenv("ZELLIJ") != "" {
		return "zellij"
	}
	if getenv("STY") != "" {
		if backend == "screen" {
			return "byobu (screen)"
		}
		return "screen"
	}
	return "none"
}

func sshWrapRecommendation(getenv func(string) string, ssh bool) string {
	if !ssh {
		return ""
	}
	if getenv("GROK_OSC52_SINK") != "" || getenv("LC_GROK_OSC52_SINK") != "" {
		return ""
	}
	if getenv("VSCODE_INJECTION") != "" || strings.Contains(getenv("TERM_PROGRAM"), "vscode") {
		return ""
	}
	return "Use local SSH wrapping for more reliable clipboard copy and terminal recovery.\n    Run `gork wrap ssh <host>` on your local computer, or `gork doctor fix ssh-wrap` there for a persistent alias."
}

func colorSupport(getenv func(string) string, term string) string {
	if getenv("NO_COLOR") != "" {
		return "none"
	}
	colorTerm := strings.ToLower(getenv("COLORTERM"))
	if colorTerm == "truecolor" || colorTerm == "24bit" || getenv("WT_SESSION") != "" {
		return "truecolor"
	}
	if term == "" || term == "dumb" {
		return "none"
	}
	if strings.Contains(strings.ToLower(term), "256color") {
		return "256 colors"
	}
	return "basic"
}

func nativeClipboard(lookPath func(string) (string, error), goos string) (bool, string) {
	candidates := []string{"wl-copy", "xclip", "xsel"}
	if goos == "darwin" {
		candidates = []string{"pbcopy"}
	} else if goos == "windows" {
		candidates = []string{"clip"}
	}
	for _, candidate := range candidates {
		if _, err := lookPath(candidate); err == nil {
			return true, candidate
		}
	}
	return false, ""
}

func terminalWarnings(term, color, multiplexer string, clipboard, osc52 bool) []string {
	var warnings []string
	if term == "" {
		warnings = append(warnings, "TERM is not set; terminal capabilities cannot be detected.")
	} else if term == "dumb" {
		warnings = append(warnings, "TERM=dumb disables interactive terminal features.")
	}
	if color == "basic" {
		warnings = append(warnings, "Limited color support; set COLORTERM=truecolor when your terminal supports it.")
	}
	if strings.Contains(multiplexer, "tmux") && !strings.Contains(strings.ToLower(term), "256color") {
		warnings = append(warnings, "tmux is not advertising 256 colors; use tmux-256color as its default terminal.")
	}
	if !clipboard && !osc52 {
		warnings = append(warnings, "No clipboard route is available; install a native clipboard tool or enable OSC 52.")
	}
	return warnings
}

func yesNo(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}

func activeOff(value bool) string {
	if value {
		return "active"
	}
	return "off"
}
