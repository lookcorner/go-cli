//go:build !cgo && !linux

package voice

import (
	"context"
	"errors"
)

type platformRecorder struct{}

func (platformRecorder) Start(context.Context, int, chan<- []byte) (capture, error) {
	return nil, errors.New("microphone capture is unavailable in this CGO-disabled build")
}
