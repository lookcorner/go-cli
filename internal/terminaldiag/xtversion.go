package terminaldiag

import (
	"strings"
	"sync"
)

// xtversion holds the latest XTVERSION self-report (e.g. "WezTerm 20240203").
// Populated by the TUI probe (tea.RequestTerminalVersion) for SSH brand recovery.
var (
	xtversionMu    sync.RWMutex
	xtversionValue string
	xtversionSet   bool
)

// RecordXTVERSION stores a sanitized terminal self-report from XTVERSION.
// Empty or control-only payloads clear any prior identification.
func RecordXTVERSION(payload string) {
	cleaned := sanitizeXTVERSION(payload)
	xtversionMu.Lock()
	defer xtversionMu.Unlock()
	xtversionValue = cleaned
	xtversionSet = true
}

// XTVERSION returns the recorded self-report, if any.
func XTVERSION() (string, bool) {
	xtversionMu.RLock()
	defer xtversionMu.RUnlock()
	if !xtversionSet || xtversionValue == "" {
		return "", false
	}
	return xtversionValue, true
}

// ResetXTVERSIONForTest clears recorded XTVERSION state.
func ResetXTVERSIONForTest() {
	xtversionMu.Lock()
	defer xtversionMu.Unlock()
	xtversionValue = ""
	xtversionSet = false
}

func sanitizeXTVERSION(payload string) string {
	var b strings.Builder
	for _, r := range payload {
		if r >= 32 && r != 127 {
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}

func xtversionIsWezTerm(payload string) bool {
	return strings.HasPrefix(strings.TrimSpace(payload), "WezTerm")
}

// ShouldProbeXTVERSION mirrors the reference crush-style gate: unknown or
// allowlisted brands, and CSI-intercepting multiplexers skipped.
func ShouldProbeXTVERSION(getenv func(string) string) bool {
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	term := strings.TrimSpace(getenv("TERM"))
	brand := normalizeBrandKey(terminalBrand(getenv, term))
	switch brand {
	case "unknown", "kitty", "wezterm", "ghostty", "iterm2", "rio":
	default:
		// Plain xterm* TERM values are Rust Unknown equivalents over SSH.
		if !strings.HasPrefix(brand, "xterm") && brand != "vt100" && brand != "vt220" && brand != "ansi" && brand != "" {
			return false
		}
	}
	switch terminalMultiplexer(getenv) {
	case "none":
		return true
	default:
		return false
	}
}
