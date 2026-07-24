//go:build !cgo && !linux

package voice

func Supported() bool { return false }
