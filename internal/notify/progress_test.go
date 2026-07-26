package notify

import (
	"testing"
	"time"
)

func TestProgressSupportRequiresCapableTerminal(t *testing.T) {
	for _, test := range []struct {
		terminal Terminal
		want     bool
	}{
		{Terminal{Brand: "ghostty"}, true},
		{Terminal{Brand: "WezTerm"}, true},
		{Terminal{Brand: "iTerm.app", Version: "3.6"}, true},
		{Terminal{Brand: "iterm2", Version: "3.6.2"}, true},
		{Terminal{Brand: "iterm2", Version: "3.5.11"}, false},
		{Terminal{Brand: "iterm2"}, false},
		{Terminal{Brand: "kitty"}, false},
		{Terminal{}, false},
	} {
		if got := ProgressSupported(test.terminal); got != test.want {
			t.Errorf("%+v: supported=%v want %v", test.terminal, got, test.want)
		}
	}
}

func TestProgressSequenceWrapsOnlyForwardingTmux(t *testing.T) {
	ghostty := Terminal{Brand: "ghostty"}
	if got, want := ProgressSequence(true, ghostty), "\x1b]9;4;1;-1\x07"; got != want {
		t.Errorf("active=%q want %q", got, want)
	}
	if got, want := ProgressSequence(false, ghostty), "\x1b]9;4;0;0\x07"; got != want {
		t.Errorf("clear=%q want %q", got, want)
	}
	if got := ProgressSequence(true, Terminal{Brand: "kitty"}); got != "" {
		t.Errorf("unsupported terminal=%q want empty", got)
	}

	old := Terminal{Brand: "ghostty", Multiplexer: "tmux", MuxVersion: "tmux 3.2"}
	if got, want := ProgressSequence(true, old), "\x1b]9;4;1;-1\x07"; got != want {
		t.Errorf("tmux 3.2=%q want unwrapped %q", got, want)
	}
	if got := ProgressSequence(true, Terminal{Brand: "ghostty", Multiplexer: "tmux"}); got != "\x1b]9;4;1;-1\x07" {
		t.Errorf("unknown tmux version wrapped: %q", got)
	}
	forwarding := Terminal{Brand: "ghostty", Multiplexer: "tmux", MuxVersion: "tmux 3.4"}
	if got, want := ProgressSequence(true, forwarding), "\x1bPtmux;\x1b\x1b]9;4;1;-1\x07\x1b\\"; got != want {
		t.Errorf("tmux 3.4=%q want %q", got, want)
	}
}

func TestProgressEmitsOnChangeAndKeepalive(t *testing.T) {
	start := time.Unix(2000, 0)
	progress := NewProgress(true, Terminal{Brand: "ghostty"})

	if got := progress.Tick(false, start); got != "" {
		t.Fatalf("idle start emitted %q", got)
	}
	if got := progress.Tick(true, start); got != "\x1b]9;4;1;-1\x07" {
		t.Fatalf("busy start=%q", got)
	}
	if got := progress.Tick(true, start.Add(4*time.Second)); got != "" {
		t.Fatalf("re-emitted inside keepalive: %q", got)
	}
	if got := progress.Tick(true, start.Add(ProgressKeepalive)); got != "\x1b]9;4;1;-1\x07" {
		t.Fatalf("keepalive=%q", got)
	}
	if got := progress.Tick(false, start.Add(9*time.Second)); got != "\x1b]9;4;0;0\x07" {
		t.Fatalf("turn end=%q", got)
	}
	if got := progress.Tick(false, start.Add(10*time.Second)); got != "" {
		t.Fatalf("repeated clear=%q", got)
	}

	disabled := NewProgress(false, Terminal{Brand: "ghostty"})
	if got := disabled.Tick(true, start); got != "" {
		t.Fatalf("disabled tracker emitted %q", got)
	}
	if got := disabled.Clear(); got != "" {
		t.Fatalf("disabled clear=%q", got)
	}
}
