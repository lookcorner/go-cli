package notify

import "time"

// ProgressKeepalive is how often an active indicator is re-sent; Ghostty drops
// the OSC 9;4 bar after roughly fifteen seconds of silence.
const ProgressKeepalive = 5 * time.Second

const (
	progressIndeterminate = "\x1b]9;4;1;-1\x07"
	progressClear         = "\x1b]9;4;0;0\x07"
)

// ProgressSupported reports whether a terminal renders the OSC 9;4 tab progress
// bar. iTerm2 gained it in 3.6; older versions render the parameters as alert
// text instead, so they are treated as unsupported.
func ProgressSupported(terminal Terminal) bool {
	switch normalizeBrand(terminal.Brand) {
	case "ghostty", "wezterm":
		return true
	case "iterm", "iterm2", "itermapp":
		return versionAtLeast(terminal.Version, 3, 6)
	default:
		return false
	}
}

// ProgressSequence builds the escape for a progress state, or "" when the
// terminal cannot render one. Unlike notifications, tmux passthrough is only
// used when the server is new enough to forward it reliably.
func ProgressSequence(active bool, terminal Terminal) string {
	if !ProgressSupported(terminal) {
		return ""
	}
	sequence := progressClear
	if active {
		sequence = progressIndeterminate
	}
	if terminal.Multiplexer == "tmux" && versionAtLeast(terminal.MuxVersion, 3, 3) {
		return "\x1bPtmux;" + escapeDoubled(sequence) + "\x1b\\"
	}
	return sequence
}

// Progress tracks tab-progress state so the indicator is emitted on change and
// re-sent on the keep-alive interval while a turn runs.
type Progress struct {
	enabled  bool
	terminal Terminal
	active   bool
	lastSent time.Time
}

// NewProgress builds a progress tracker; a disabled tracker never emits.
func NewProgress(enabled bool, terminal Terminal) *Progress {
	return &Progress{enabled: enabled, terminal: terminal}
}

// Tick returns the escape to write for the current busy state, or "" when
// nothing needs to change.
func (p *Progress) Tick(busy bool, now time.Time) string {
	if !p.enabled {
		return ""
	}
	if !busy {
		return p.Clear()
	}
	if p.active && now.Sub(p.lastSent) < ProgressKeepalive {
		return ""
	}
	p.active, p.lastSent = true, now
	return ProgressSequence(true, p.terminal)
}

// Clear returns the escape that removes an active indicator, or "" when none is
// showing.
func (p *Progress) Clear() string {
	if !p.active {
		return ""
	}
	p.active, p.lastSent = false, time.Time{}
	return ProgressSequence(false, p.terminal)
}
