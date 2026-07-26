// Package notify owns desktop-notification policy: which terminal protocol
// carries a notification, when focus and event rules allow one, and the exact
// escape sequence to write. Adapters supply terminal identity and perform I/O.
package notify

import (
	"slices"
	"strconv"
	"strings"
	"time"
)

// Protocol is a resolved notification wire format.
type Protocol string

const (
	ProtocolOSC9   Protocol = "osc9"
	ProtocolOSC99  Protocol = "osc99"
	ProtocolOSC777 Protocol = "osc777"
	ProtocolBel    Protocol = "bel"
	ProtocolNone   Protocol = "none"
)

// Event identifies a notifiable moment in a session.
type Event string

const (
	TurnComplete     Event = "turn_complete"
	ApprovalRequired Event = "approval_required"
	SessionReady     Event = "session_ready"
	TaskComplete     Event = "task_complete"
	AgentError       Event = "agent_error"
)

// Events lists every supported event in reference order.
var Events = []Event{TurnComplete, ApprovalRequired, SessionReady, TaskComplete, AgentError}

// Label is the human-readable notification body for an event.
func (e Event) Label() string {
	switch e {
	case TurnComplete:
		return "Turn complete"
	case ApprovalRequired:
		return "Approval required"
	case SessionReady:
		return "Session ready"
	case TaskComplete:
		return "Task complete"
	case AgentError:
		return "Agent error"
	default:
		return string(e)
	}
}

// Conditions gating emission on terminal focus.
const (
	ConditionUnfocused = "unfocused"
	ConditionAlways    = "always"
	ConditionNever     = "never"
)

// Methods a user may configure; auto resolves from terminal identity.
const (
	MethodAuto = "auto"
	MethodNone = "none"
)

// Terminal is the host terminal identity protocol selection depends on.
type Terminal struct {
	Brand       string // normalized brand key, e.g. "kitty", "ghostty"
	Multiplexer string // "tmux", "zellij", "screen", or ""
}

// DetectTerminal resolves terminal identity from the environment.
func DetectTerminal(lookup func(string) (string, bool)) Terminal {
	value := func(name string) string {
		got, _ := lookup(name)
		return strings.TrimSpace(got)
	}
	terminal := Terminal{}
	switch {
	case value("TMUX") != "":
		terminal.Multiplexer = "tmux"
	case value("ZELLIJ") != "" || value("ZELLIJ_SESSION_NAME") != "":
		terminal.Multiplexer = "zellij"
	case value("STY") != "":
		terminal.Multiplexer = "screen"
	}
	terminal.Brand = detectBrand(value)
	return terminal
}

// detectBrand mirrors the reference brand table using the same environment
// hints the hyperlink and image adapters already rely on.
func detectBrand(value func(string) string) string {
	if brand := normalizeBrand(value("TERM_PROGRAM")); brand != "" {
		return brand
	}
	switch {
	case value("KITTY_WINDOW_ID") != "":
		return "kitty"
	case value("GHOSTTY_RESOURCES_DIR") != "":
		return "ghostty"
	case value("WEZTERM_EXECUTABLE") != "":
		return "wezterm"
	case value("WT_SESSION") != "":
		return "windowsterminal"
	case value("TERMINATOR_UUID") != "":
		return "terminator"
	}
	if version, err := strconv.Atoi(value("VTE_VERSION")); err == nil && version > 0 {
		return "vte"
	}
	term := strings.ToLower(value("TERM"))
	for _, brand := range []string{"kitty", "alacritty", "wezterm", "rio", "ghostty"} {
		if strings.Contains(term, brand) {
			return brand
		}
	}
	if strings.HasPrefix(term, "foot") {
		return "foot"
	}
	return ""
}

// normalizeBrand folds separators and case so "iTerm.app" and "iterm2" agree.
func normalizeBrand(value string) string {
	return strings.Map(func(char rune) rune {
		if char == ' ' || char == '-' || char == '_' || char == '.' {
			return -1
		}
		return char
	}, strings.ToLower(strings.TrimSpace(value)))
}

// SelectProtocol picks the best protocol for a terminal. Zellij cannot forward
// OSC notifications, so it falls back to the bell regardless of brand.
func SelectProtocol(terminal Terminal) Protocol {
	if terminal.Multiplexer == "zellij" {
		return ProtocolBel
	}
	switch normalizeBrand(terminal.Brand) {
	case "iterm", "iterm2", "itermapp", "wezterm", "warp", "warpterminal":
		return ProtocolOSC9
	case "kitty":
		return ProtocolOSC99
	case "ghostty", "vte", "terminator", "foot":
		return ProtocolOSC777
	case "grokdesktop":
		return ProtocolNone
	default:
		return ProtocolBel
	}
}

// ResolveProtocol applies a configured method over terminal detection.
func ResolveProtocol(method string, terminal Terminal) Protocol {
	switch Protocol(strings.ToLower(strings.TrimSpace(method))) {
	case ProtocolOSC9:
		return ProtocolOSC9
	case ProtocolOSC99:
		return ProtocolOSC99
	case ProtocolOSC777:
		return ProtocolOSC777
	case ProtocolBel:
		return ProtocolBel
	case ProtocolNone:
		return ProtocolNone
	default:
		return SelectProtocol(terminal)
	}
}

// Sequence builds the escape sequence for a protocol. Body-only protocols fold
// the title in; OSC 777 already shows the tab title as its subtitle, so it
// carries the application name instead. Returns "" when nothing is emitted.
func Sequence(protocol Protocol, title, body string, tmux bool) string {
	var sequence string
	switch protocol {
	case ProtocolOSC9:
		sequence = "\x1b]9;" + body + " · " + title + "\x07"
	case ProtocolOSC99:
		sequence = "\x1b]99;i=grok;" + body + " · " + title + "\x1b\\"
	case ProtocolOSC777:
		sequence = "\x1b]777;notify;Grok;" + body + "\x1b\\"
	case ProtocolBel:
		sequence = "\x07"
	default:
		return ""
	}
	if tmux {
		return "\x1bPtmux;" + strings.ReplaceAll(sequence, "\x1b", "\x1b\x1b") + "\x1b\\"
	}
	return sequence
}

// Settings is the resolved [ui.notifications] policy a session runs under.
type Settings struct {
	Method        string
	Condition     string
	IdleThreshold time.Duration
	Events        []Event
}

// Notifier applies notification policy against live terminal focus.
type Notifier struct {
	settings Settings
	terminal Terminal
	protocol Protocol
	focused  bool
	lostAt   time.Time
}

// New builds a notifier with the terminal reported as focused.
func New(settings Settings, terminal Terminal) *Notifier {
	return &Notifier{
		settings: settings,
		terminal: terminal,
		protocol: ResolveProtocol(settings.Method, terminal),
		focused:  true,
	}
}

// Protocol reports the resolved wire protocol.
func (n *Notifier) Protocol() Protocol { return n.protocol }

// Focused reports the last known terminal focus state.
func (n *Notifier) Focused() bool { return n.focused }

// Focus records that the terminal regained focus.
func (n *Notifier) Focus() {
	n.focused = true
	n.lostAt = time.Time{}
}

// Blur records that the terminal lost focus, starting the idle clock.
func (n *Notifier) Blur(now time.Time) {
	n.focused = false
	n.lostAt = now
}

// Enabled reports whether any notification can still be emitted.
func (n *Notifier) Enabled() bool {
	return n.protocol != ProtocolNone && n.settings.Condition != ConditionNever && len(n.settings.Events) > 0
}

// NeedsFocusReports reports whether focus tracking changes any outcome.
func (n *Notifier) NeedsFocusReports() bool {
	return n.Enabled() && n.settings.Condition == ConditionUnfocused
}

// Sequence returns the bytes to write for an event, or "" when policy
// suppresses it. Unfocused gating requires the idle threshold to have elapsed.
func (n *Notifier) Sequence(event Event, title, body string, now time.Time) string {
	if n.protocol == ProtocolNone || !slices.Contains(n.settings.Events, event) {
		return ""
	}
	switch n.settings.Condition {
	case ConditionNever:
		return ""
	case ConditionAlways:
	default:
		if n.focused || n.lostAt.IsZero() || now.Sub(n.lostAt) < n.settings.IdleThreshold {
			return ""
		}
	}
	return Sequence(n.protocol, title, body, n.terminal.Multiplexer == "tmux")
}
