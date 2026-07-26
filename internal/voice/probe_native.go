//go:build cgo && !linux

package voice

import "github.com/gen2brain/malgo"

func platformProbeInput(func(string) (string, error)) InputProbe {
	audioContext, err := malgo.InitContext(nil, malgo.ContextConfig{}, nil)
	if err != nil {
		return InputProbe{Error: err.Error()}
	}
	defer func() {
		_ = audioContext.Uninit()
		audioContext.Free()
	}()
	devices, err := audioContext.Devices(malgo.Capture)
	if err != nil {
		return InputProbe{Error: err.Error()}
	}
	if len(devices) == 0 {
		return InputProbe{Error: "no microphone input device found"}
	}
	chosen := devices[0]
	for _, device := range devices {
		if device.IsDefault != 0 {
			chosen = device
			break
		}
	}
	name := chosen.Name()
	if name == "" {
		name = "default"
	}
	return InputProbe{Name: name, Detail: "default capture device"}
}
