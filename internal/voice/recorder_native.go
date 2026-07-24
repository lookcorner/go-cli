//go:build cgo && !linux

package voice

import (
	"context"
	"fmt"
	"sync"

	"github.com/gen2brain/malgo"
)

type platformRecorder struct{}

func (platformRecorder) Start(ctx context.Context, sampleRate int, output chan<- []byte) (capture, error) {
	audioContext, err := malgo.InitContext(nil, malgo.ContextConfig{}, nil)
	if err != nil {
		return nil, err
	}
	config := malgo.DefaultDeviceConfig(malgo.Capture)
	config.Capture.Format = malgo.FormatS16
	config.Capture.Channels = 1
	config.SampleRate = uint32(sampleRate)
	device, err := malgo.InitDevice(audioContext.Context, config, malgo.DeviceCallbacks{
		Data: func(_, input []byte, _ uint32) {
			if len(input) == 0 {
				return
			}
			chunk := append([]byte(nil), input...)
			select {
			case output <- chunk:
			default:
			}
		},
	})
	if err != nil {
		_ = audioContext.Uninit()
		audioContext.Free()
		return nil, err
	}
	if err := device.Start(); err != nil {
		device.Uninit()
		_ = audioContext.Uninit()
		audioContext.Free()
		return nil, fmt.Errorf("open default input device: %w", err)
	}
	handle := &nativeCapture{device: device, context: audioContext}
	go func() {
		<-ctx.Done()
		handle.Stop()
	}()
	return handle, nil
}

type nativeCapture struct {
	device  *malgo.Device
	context *malgo.AllocatedContext
	once    sync.Once
}

func (c *nativeCapture) Stop() {
	c.once.Do(func() {
		_ = c.device.Stop()
		c.device.Uninit()
		_ = c.context.Uninit()
		c.context.Free()
	})
}
