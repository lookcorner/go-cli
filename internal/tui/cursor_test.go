package tui

import (
	"strings"
	"testing"
)

func TestCursorBlinkSequence(t *testing.T) {
	if got := cursorBlinkSequence(true); got != "\x1b[?12h\x1b[1 q" {
		t.Fatalf("blinking sequence=%q", got)
	}
	if got := cursorBlinkSequence(false); got != "\x1b[?12l\x1b[2 q" {
		t.Fatalf("steady sequence=%q", got)
	}
}

func TestApplyCursorBlinkPreservesOrRestoresTerminalStyle(t *testing.T) {
	for _, test := range []struct {
		name  string
		blink *bool
		want  string
	}{
		{name: "inherit"},
		{name: "blinking", blink: boolPointer(true), want: "\x1b[?12h\x1b[1 q\x1b[0 q"},
		{name: "steady", blink: boolPointer(false), want: "\x1b[?12l\x1b[2 q\x1b[0 q"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output strings.Builder
			restore := applyCursorBlink(&output, test.blink)
			restore()
			if got := output.String(); got != test.want {
				t.Fatalf("output=%q want=%q", got, test.want)
			}
		})
	}
}
