package tui

import "io"

const resetCursorStyle = "\x1b[0 q"

func applyCursorBlink(output io.Writer, blink *bool) func() {
	if blink == nil {
		return func() {}
	}
	restoreOutput, ok := enableCursorSequences(output)
	if !ok {
		return func() {}
	}
	_, _ = io.WriteString(output, cursorBlinkSequence(*blink))
	return func() {
		_, _ = io.WriteString(output, resetCursorStyle)
		restoreOutput()
	}
}

func cursorBlinkSequence(blink bool) string {
	if blink {
		return "\x1b[?12h\x1b[1 q"
	}
	return "\x1b[?12l\x1b[2 q"
}
