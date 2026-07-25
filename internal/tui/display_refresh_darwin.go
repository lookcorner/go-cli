//go:build darwin && cgo

package tui

/*
#cgo LDFLAGS: -framework CoreGraphics
#include <CoreGraphics/CoreGraphics.h>
*/
import "C"

func probeDisplayRefreshHz() (uint32, bool) {
	mode := C.CGDisplayCopyDisplayMode(C.CGMainDisplayID())
	if mode == 0 {
		return 0, false
	}
	defer C.CGDisplayModeRelease(mode)
	rate := float64(C.CGDisplayModeGetRefreshRate(mode))
	if rate < 2 || rate > 1000 {
		return 0, false
	}
	return uint32(rate + 0.5), true
}
