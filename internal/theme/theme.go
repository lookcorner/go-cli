package theme

import "strings"

var Names = [...]string{"groknight", "grokday", "tokyonight", "rosepine-moon", "oscura-midnight"}

func Canonical(value string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "auto", "system":
		return "auto", true
	case "groknight", "grok-night", "dark":
		return "groknight", true
	case "grokday", "grok-day", "light", "day":
		return "grokday", true
	case "tokyonight", "tokyo-night", "tokyo":
		return "tokyonight", true
	case "rosepine", "rose-pine", "rosepine-moon", "rose-pine-moon":
		return "rosepine-moon", true
	case "oscura", "oscura-midnight":
		return "oscura-midnight", true
	default:
		return "", false
	}
}
