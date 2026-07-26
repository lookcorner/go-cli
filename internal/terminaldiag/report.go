package terminaldiag

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/lookcorner/go-cli/internal/notify"
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
	ControlMode      string `json:"tmuxControlMode,omitempty"`
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
	findings := terminalFindings(getenv, term, brand, color, multiplexer, ssh, clipboard, osc52)
	var tmux tmuxProbe
	if strings.Contains(multiplexer, "byobu (screen)") {
		findings = append(findings, "Byobu is using GNU screen, which has limited clipboard and display support.\n    Switch Byobu to its tmux backend, then restart or reattach the session.")
	} else {
		tmux = collectTmuxProbe(getenv)
		findings = append(findings, tmuxProbeFindings(tmux)...)
	}
	findings = append(findings, notificationFindings(getenv)...)
	if finding := sshWrapRecommendation(getenv, ssh); finding != "" {
		findings = append(findings, finding)
	}
	return Snapshot{
		SchemaVersion: SchemaVersion,
		Facts: Facts{
			Terminal: brand, Multiplexer: multiplexer, SSH: ssh, Color: color,
			NativeClip: clipboard, ClipboardTool: clipboardTool, OSC52: osc52, GOOS: goos,
			SetClipboard: tmux.SetClipboard, AllowPassthrough: tmux.AllowPassthrough, ExtendedKeys: tmux.ExtendedKeys,
			ControlMode: tmux.ControlMode,
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
		fmt.Fprintf(&out, "  set-clipboard %s\n  allow-passthrough %s\n  extended-keys %s\n  control-mode %s\n",
			tmuxFactOrUnknown(s.Facts.SetClipboard), tmuxFactOrUnknown(s.Facts.AllowPassthrough), tmuxFactOrUnknown(s.Facts.ExtendedKeys),
			tmuxFactOrUnknown(s.Facts.ControlMode))
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
	// iTerm2's LC_TERMINAL / session markers survive SSH when TERM_PROGRAM does not.
	if getenv("ITERM_SESSION_ID") != "" || getenv("ITERM_PROFILE") != "" ||
		strings.EqualFold(strings.TrimSpace(getenv("LC_TERMINAL")), "iTerm2") {
		return "iTerm2"
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

func terminalFindings(getenv func(string) string, term, brand, color, multiplexer string, ssh, clipboard, osc52 bool) []string {
	var findings []string
	if term == "" {
		findings = append(findings, "TERM is not set; terminal capabilities cannot be detected.")
	} else if term == "dumb" {
		findings = append(findings, "TERM=dumb disables interactive terminal features.")
	}
	findings = append(findings, colorFindings(getenv, term, brand, color, multiplexer)...)
	if isAppleTerminal(brand) && ssh {
		findings = append(findings, "Apple Terminal doesn't support OSC 52, so clipboard copy over SSH is unavailable.\n    Run `gork wrap ssh <host>` on your local computer, or use a terminal that supports OSC 52. Gork also saves each copy to the backup file shown in the copy message.")
	}
	findings = append(findings, clipboardCaveatFindings(getenv, brand, ssh, clipboard, osc52)...)
	if !clipboard && !osc52 {
		findings = append(findings, "No clipboard route is available; install a native clipboard tool or enable OSC 52.")
	}
	return findings
}

// clipboardCaveatFindings mirrors reference clipboard recommendations for hosts
// that can deliver OSC 52 but may still mangle or block copies.
func clipboardCaveatFindings(getenv func(string) string, brand string, ssh, clipboard, osc52 bool) []string {
	if !osc52 || wrapSinkActive(getenv) {
		return nil
	}
	var findings []string
	if ssh && isVSCodeFamily(getenv, brand) {
		findings = append(findings, "This remote editor may change non-ASCII text copied with OSC 52.\n    If pasted non-ASCII text is incorrect, use `/minimal` and select text in the terminal. ASCII copy and the backup file shown after the copy remain available.")
	}
	if isITerm2(brand) && (ssh || !clipboard) {
		findings = append(findings, "iTerm2 may block OSC 52 clipboard access.\n    In iTerm2, open Settings → General → Selection and turn on “Applications in terminal may access clipboard.” Grok can't read this setting, so check it there if copies don't paste.")
	}
	return findings
}

func wrapSinkActive(getenv func(string) string) bool {
	return getenv("GROK_OSC52_SINK") != "" || getenv("LC_GROK_OSC52_SINK") != ""
}

func isITerm2(brand string) bool {
	switch normalizeBrandKey(brand) {
	case "iterm", "iterm2", "itermapp":
		return true
	default:
		return false
	}
}

func isVSCodeFamily(getenv func(string) string, brand string) bool {
	switch normalizeBrandKey(brand) {
	case "vscode", "cursor", "windsurf", "zed":
		return true
	}
	if getenv("CURSOR_TRACE_ID") != "" {
		return true
	}
	return getenv("VSCODE_GIT_ASKPASS_MAIN") != ""
}

func normalizeBrandKey(brand string) string {
	return strings.Map(func(char rune) rune {
		if char == ' ' || char == '-' || char == '_' || char == '.' {
			return -1
		}
		return char
	}, strings.ToLower(strings.TrimSpace(brand)))
}

func colorFindings(getenv func(string) string, term, brand, color, multiplexer string) []string {
	if getenv("NO_COLOR") != "" {
		return []string{"Colors are off because `NO_COLOR` is set.\n    Unset `NO_COLOR`, then restart Gork."}
	}
	if isAppleTerminal(brand) && color != "truecolor" && color != "none" {
		return []string{"Apple Terminal supports 256 colors, so truecolor themes are unavailable.\n    Use a terminal that supports truecolor, such as Ghostty."}
	}
	var findings []string
	if color == "basic" {
		findings = append(findings, "Limited color support; set COLORTERM=truecolor when your terminal supports it.\n    → Automatic setup: `gork doctor fix colorterm`")
	}
	if strings.Contains(multiplexer, "tmux") && !strings.Contains(strings.ToLower(term), "256color") {
		findings = append(findings, "tmux is not advertising 256 colors; use tmux-256color as its default terminal.\n    → Automatic setup: `gork doctor fix tmux-truecolor`")
	}
	return findings
}

func isAppleTerminal(brand string) bool {
	normalized := strings.ToLower(strings.TrimSpace(brand))
	return normalized == "apple_terminal" || normalized == "apple terminal"
}

// notificationFindings mirrors reference collect_notification_warnings for the
// default [ui.notifications] policy (method=auto, condition=unfocused).
func notificationFindings(getenv func(string) string) []string {
	terminal := notify.DetectTerminal(func(name string) (string, bool) {
		value := getenv(name)
		return value, value != ""
	})
	protocol := notify.SelectProtocol(terminal)
	var findings []string
	if protocol == notify.ProtocolBel && !recognizedTerminalBrand(terminal.Brand) {
		findings = append(findings, "Grok is using the terminal bell because the terminal was not recognized.\n    If the bell works for you, no change is needed. Otherwise, set `method` in `[ui.notifications]` in config.toml to a protocol your terminal supports. Set it to `none` to turn off terminal notifications.")
	}
	if !supportsFocusTracking(terminal.Brand) {
		findings = append(findings, "This terminal may not report focus changes, so notifications set to `unfocused` may not appear.\n    Use `condition = \"always\"` in `[ui.notifications]` to notify whether or not the terminal is focused. Use `never` or `method = \"none\"` to turn notifications off.")
	}
	return findings
}

func recognizedTerminalBrand(brand string) bool {
	switch brand {
	case "appleterminal", "ghostty", "iterm", "iterm2", "itermapp", "warp", "warpterminal",
		"vscode", "cursor", "windsurf", "zed", "wezterm", "kitty", "alacritty",
		"rio", "foot", "jetbrains", "grokdesktop", "vte", "terminator",
		"windowsterminal", "otty":
		return true
	default:
		return false
	}
}

// supportsFocusTracking matches reference CSI focus-tracking support: known
// non-Apple brands report focus; Apple Terminal and unclassified brands do not.
func supportsFocusTracking(brand string) bool {
	if brand == "" || brand == "appleterminal" || brand == "otty" {
		return false
	}
	return recognizedTerminalBrand(brand)
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
