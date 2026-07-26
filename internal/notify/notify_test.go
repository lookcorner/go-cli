package notify

import (
	"testing"
	"time"
)

func TestSelectProtocolCoversEveryBrand(t *testing.T) {
	for brand, want := range map[string]Protocol{
		"iTerm.app": ProtocolOSC9, "iterm2": ProtocolOSC9, "WezTerm": ProtocolOSC9,
		"WarpTerminal": ProtocolOSC9, "kitty": ProtocolOSC99, "ghostty": ProtocolOSC777,
		"vte": ProtocolOSC777, "terminator": ProtocolOSC777, "foot": ProtocolOSC777,
		"Grok Desktop": ProtocolNone, "Apple_Terminal": ProtocolBel, "alacritty": ProtocolBel,
		"vscode": ProtocolBel, "windowsterminal": ProtocolBel, "": ProtocolBel,
	} {
		if got := SelectProtocol(Terminal{Brand: brand}); got != want {
			t.Errorf("brand %q: protocol=%q want %q", brand, got, want)
		}
		if got := SelectProtocol(Terminal{Brand: brand, Multiplexer: "zellij"}); got != ProtocolBel {
			t.Errorf("brand %q under zellij: protocol=%q want bel", brand, got)
		}
		if got := SelectProtocol(Terminal{Brand: brand, Multiplexer: "tmux"}); got != want {
			t.Errorf("brand %q under tmux: protocol=%q want %q", brand, got, want)
		}
	}
}

func TestResolveProtocolOverridesDetection(t *testing.T) {
	kitty := Terminal{Brand: "kitty"}
	if got := ResolveProtocol("auto", kitty); got != ProtocolOSC99 {
		t.Fatalf("auto=%q", got)
	}
	for method, want := range map[string]Protocol{
		"osc9": ProtocolOSC9, "osc99": ProtocolOSC99, "osc777": ProtocolOSC777,
		"bel": ProtocolBel, "none": ProtocolNone, "": ProtocolOSC99, "bogus": ProtocolOSC99,
	} {
		if got := ResolveProtocol(method, kitty); got != want {
			t.Errorf("method %q: protocol=%q want %q", method, got, want)
		}
	}
}

func TestSequenceMatchesReferenceBytes(t *testing.T) {
	for protocol, want := range map[Protocol]string{
		ProtocolOSC9:   "\x1b]9;done · session\x07",
		ProtocolOSC99:  "\x1b]99;i=grok;done · session\x1b\\",
		ProtocolOSC777: "\x1b]777;notify;Grok;done\x1b\\",
		ProtocolBel:    "\x07",
		ProtocolNone:   "",
	} {
		if got := Sequence(protocol, "session", "done", false); got != want {
			t.Errorf("%s: %q want %q", protocol, got, want)
		}
	}
}

func TestSequenceWrapsTmuxPassthroughAndDoublesEscapes(t *testing.T) {
	if got, want := Sequence(ProtocolOSC9, "s", "b", true), "\x1bPtmux;\x1b\x1b]9;b · s\x07\x1b\\"; got != want {
		t.Errorf("osc9 tmux=%q want %q", got, want)
	}
	if got, want := Sequence(ProtocolOSC777, "s", "b", true), "\x1bPtmux;\x1b\x1b]777;notify;Grok;b\x1b\x1b\\\x1b\\"; got != want {
		t.Errorf("osc777 tmux=%q want %q", got, want)
	}
	if got, want := Sequence(ProtocolBel, "", "", true), "\x1bPtmux;\x07\x1b\\"; got != want {
		t.Errorf("bel tmux=%q want %q", got, want)
	}
	if got := Sequence(ProtocolNone, "s", "b", true); got != "" {
		t.Errorf("none tmux=%q want empty", got)
	}
}

func TestNotifierGatesOnEventsAndFocus(t *testing.T) {
	base := Settings{Condition: ConditionUnfocused, IdleThreshold: 3 * time.Second, Events: []Event{TurnComplete}}
	start := time.Unix(1000, 0)

	unfocused := New(base, Terminal{Brand: "kitty"})
	if got := unfocused.Sequence(TurnComplete, "s", "b", start); got != "" {
		t.Fatalf("focused terminal emitted %q", got)
	}
	unfocused.Blur(start)
	if got := unfocused.Sequence(TurnComplete, "s", "b", start.Add(2*time.Second)); got != "" {
		t.Fatalf("emitted before idle threshold: %q", got)
	}
	if got := unfocused.Sequence(AgentError, "s", "b", start.Add(5*time.Second)); got != "" {
		t.Fatalf("emitted unconfigured event: %q", got)
	}
	if got := unfocused.Sequence(TurnComplete, "s", "b", start.Add(3*time.Second)); got == "" {
		t.Fatal("no emission at idle threshold")
	}
	unfocused.Focus()
	if got := unfocused.Sequence(TurnComplete, "s", "b", start.Add(9*time.Second)); got != "" {
		t.Fatalf("emitted after refocus: %q", got)
	}

	always := New(Settings{Condition: ConditionAlways, Events: []Event{TurnComplete}}, Terminal{Brand: "kitty"})
	if got := always.Sequence(TurnComplete, "s", "b", start); got == "" {
		t.Fatal("always condition suppressed a focused emission")
	}
	never := New(Settings{Condition: ConditionNever, Events: []Event{TurnComplete}}, Terminal{Brand: "kitty"})
	never.Blur(start)
	if got := never.Sequence(TurnComplete, "s", "b", start.Add(time.Hour)); got != "" {
		t.Fatalf("never condition emitted %q", got)
	}
	silent := New(base, Terminal{Brand: "Grok Desktop"})
	silent.Blur(start)
	if got := silent.Sequence(TurnComplete, "s", "b", start.Add(time.Hour)); got != "" {
		t.Fatalf("incapable terminal emitted %q", got)
	}
}

func TestNotifierReportsEnablementAndFocusNeed(t *testing.T) {
	events := []Event{TurnComplete}
	for _, test := range []struct {
		name           string
		settings       Settings
		terminal       Terminal
		enabled, focus bool
	}{
		{"unfocused", Settings{Condition: ConditionUnfocused, Events: events}, Terminal{Brand: "kitty"}, true, true},
		{"always", Settings{Condition: ConditionAlways, Events: events}, Terminal{Brand: "kitty"}, true, false},
		{"never", Settings{Condition: ConditionNever, Events: events}, Terminal{Brand: "kitty"}, false, false},
		{"no events", Settings{Condition: ConditionUnfocused}, Terminal{Brand: "kitty"}, false, false},
		{"method none", Settings{Method: MethodNone, Condition: ConditionUnfocused, Events: events}, Terminal{Brand: "kitty"}, false, false},
		{"incapable", Settings{Condition: ConditionUnfocused, Events: events}, Terminal{Brand: "Grok Desktop"}, false, false},
	} {
		notifier := New(test.settings, test.terminal)
		if notifier.Enabled() != test.enabled || notifier.NeedsFocusReports() != test.focus {
			t.Errorf("%s: enabled=%v want %v focus=%v want %v", test.name, notifier.Enabled(), test.enabled, notifier.NeedsFocusReports(), test.focus)
		}
	}
	if notifier := New(Settings{}, Terminal{}); !notifier.Focused() {
		t.Error("new notifier is not focused")
	}
}

func TestDetectTerminalReadsEnvironment(t *testing.T) {
	env := func(pairs map[string]string) func(string) (string, bool) {
		return func(name string) (string, bool) {
			value, ok := pairs[name]
			return value, ok
		}
	}
	for _, test := range []struct {
		name string
		vars map[string]string
		want Terminal
	}{
		{"term program wins", map[string]string{"TERM_PROGRAM": "iTerm.app", "TERM": "xterm-kitty"}, Terminal{Brand: "itermapp"}},
		{"kitty id", map[string]string{"KITTY_WINDOW_ID": "1"}, Terminal{Brand: "kitty"}},
		{"ghostty resources", map[string]string{"GHOSTTY_RESOURCES_DIR": "/x"}, Terminal{Brand: "ghostty"}},
		{"windows terminal", map[string]string{"WT_SESSION": "abc"}, Terminal{Brand: "windowsterminal"}},
		{"vte version", map[string]string{"VTE_VERSION": "7600"}, Terminal{Brand: "vte"}},
		{"term fallback", map[string]string{"TERM": "foot-extra"}, Terminal{Brand: "foot"}},
		{"tmux", map[string]string{"TMUX": "/tmp/s", "TERM_PROGRAM": "ghostty"}, Terminal{Brand: "ghostty", Multiplexer: "tmux"}},
		{"zellij", map[string]string{"ZELLIJ": "0", "KITTY_WINDOW_ID": "2"}, Terminal{Brand: "kitty", Multiplexer: "zellij"}},
		{"screen", map[string]string{"STY": "1.pts", "TERM": ""}, Terminal{Multiplexer: "screen"}},
		{"unknown", map[string]string{}, Terminal{}},
	} {
		if got := DetectTerminal(env(test.vars)); got != test.want {
			t.Errorf("%s: terminal=%+v want %+v", test.name, got, test.want)
		}
	}
}
