package terminaldiag

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

func IsCommand(prompt string) bool {
	fields := strings.Fields(prompt)
	if len(fields) == 0 {
		return false
	}
	switch fields[0] {
	case "/terminal-setup", "/terminal-check", "/terminal-info":
		return true
	default:
		return false
	}
}

func Report() string {
	return buildReport(os.Getenv, exec.LookPath, runtime.GOOS)
}

func buildReport(getenv func(string) string, lookPath func(string) (string, error), goos string) string {
	term := strings.TrimSpace(getenv("TERM"))
	brand := terminalBrand(getenv, term)
	multiplexer := terminalMultiplexer(getenv)
	ssh := getenv("SSH_CONNECTION") != "" || getenv("SSH_TTY") != ""
	color := colorSupport(getenv, term)
	clipboard, clipboardTool := nativeClipboard(lookPath, goos)
	osc52 := term != "dumb" && (term != "" || brand != "unknown")

	var out strings.Builder
	fmt.Fprintf(&out, "Environment\n  terminal     %s\n  multiplexer  %s\n  ssh          %s\n  color        %s\n", brand, multiplexer, yesNo(ssh), color)
	fmt.Fprintf(&out, "\nClipboard routes\n  native       %s", activeOff(clipboard))
	if clipboardTool != "" {
		fmt.Fprintf(&out, " (tool: %s)", clipboardTool)
	}
	fmt.Fprintf(&out, "\n  osc 52       %s\n", activeOff(osc52))

	warnings := terminalWarnings(term, color, multiplexer, clipboard, osc52)
	if len(warnings) == 0 {
		out.WriteString("\nNo issues found.")
		return out.String()
	}
	fmt.Fprintf(&out, "\n%d issue(s)\n", len(warnings))
	for _, warning := range warnings {
		fmt.Fprintf(&out, "\n  [!] %s", warning)
	}
	return out.String()
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
	if getenv("TMUX") != "" {
		if getenv("BYOBU_BACKEND") != "" {
			return "byobu (tmux)"
		}
		return "tmux"
	}
	if getenv("ZELLIJ") != "" {
		return "zellij"
	}
	if getenv("STY") != "" {
		return "screen"
	}
	return "none"
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
