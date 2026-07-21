package tui

import (
	"os"
	"strconv"
	"strings"
)

func detectTerminalHyperlinks() bool {
	return terminalHyperlinksEnabled(os.LookupEnv)
}

func terminalHyperlinksEnabled(lookup func(string) (string, bool)) bool {
	for _, name := range []string{"TMUX", "STY", "ZELLIJ", "ZELLIJ_SESSION_NAME"} {
		if value, ok := lookup(name); ok && strings.TrimSpace(value) != "" {
			return false
		}
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
