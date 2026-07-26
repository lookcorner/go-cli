//go:build !cgo && !linux

package voice

func platformProbeInput(func(string) (string, error)) InputProbe {
	return InputProbe{Error: "voice capture is not supported in this build"}
}
