package tui

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
)

func detectTerminalHyperlinks() bool {
	return terminalHyperlinksEnabled(os.LookupEnv, detectTmuxVersion)
}

func terminalHyperlinksEnabled(lookup func(string) (string, bool), tmuxVersion func() string) bool {
	for _, name := range []string{"STY", "ZELLIJ", "ZELLIJ_SESSION_NAME"} {
		if value, ok := lookup(name); ok && strings.TrimSpace(value) != "" {
			return false
		}
	}
	if value, ok := lookup("TMUX"); ok && strings.TrimSpace(value) != "" && !tmuxVersionAtLeast(tmuxVersion(), 3, 4) {
		return false
	}
	if value, ok := lookup("WT_SESSION"); ok && strings.TrimSpace(value) != "" {
		return true
	}
	if value, ok := lookup("VTE_VERSION"); ok {
		version, err := strconv.Atoi(value)
		return err == nil && version >= 5000
	}
	if value, ok := lookup("TERM_PROGRAM"); ok {
		brand := strings.Map(func(char rune) rune {
			if char == ' ' || char == '-' || char == '_' || char == '.' {
				return -1
			}
			return char
		}, strings.ToLower(strings.TrimSpace(value)))
		switch brand {
		case "ghostty", "iterm", "iterm2", "itermapp", "vscode", "cursor", "windsurf", "zed", "wezterm", "kitty", "alacritty", "rio", "windowsterminal":
			return true
		case "appleterminal", "warp", "warpterminal":
			return false
		}
	}
	term, _ := lookup("TERM")
	term = strings.ToLower(term)
	return strings.Contains(term, "kitty") || strings.Contains(term, "alacritty") || strings.HasPrefix(term, "foot") || strings.Contains(term, "wezterm") || strings.Contains(term, "rio")
}

func detectTmuxVersion() string {
	output, err := exec.Command("tmux", "-V").Output()
	if err != nil {
		return ""
	}
	return string(output)
}

func tmuxVersionAtLeast(value string, major, minor int) bool {
	value = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(value), "tmux"))
	parts := strings.SplitN(value, ".", 3)
	if len(parts) < 2 {
		return false
	}
	end := 0
	for end < len(parts[1]) && parts[1][end] >= '0' && parts[1][end] <= '9' {
		end++
	}
	gotMajor, majorErr := strconv.Atoi(parts[0])
	gotMinor, minorErr := strconv.Atoi(parts[1][:end])
	return majorErr == nil && minorErr == nil && (gotMajor > major || gotMajor == major && gotMinor >= minor)
}
