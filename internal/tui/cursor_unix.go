//go:build !windows

package tui

import "io"

func enableCursorSequences(io.Writer) (func(), bool) {
	return func() {}, true
}
