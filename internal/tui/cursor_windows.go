//go:build windows

package tui

import (
	"io"

	"golang.org/x/sys/windows"
)

func enableCursorSequences(output io.Writer) (func(), bool) {
	file, ok := output.(interface{ Fd() uintptr })
	if !ok {
		return nil, false
	}
	handle := windows.Handle(file.Fd())
	var mode uint32
	if windows.GetConsoleMode(handle, &mode) != nil {
		return nil, false
	}
	if windows.SetConsoleMode(handle, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING) != nil {
		return nil, false
	}
	return func() { _ = windows.SetConsoleMode(handle, mode) }, true
}
