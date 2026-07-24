//go:build linux

package voice

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"
)

type platformRecorder struct{}

func (platformRecorder) Start(ctx context.Context, sampleRate int, output chan<- []byte) (capture, error) {
	program, args, err := linuxRecorder(sampleRate)
	if err != nil {
		return nil, err
	}
	childCtx, cancel := context.WithCancel(ctx)
	command := exec.CommandContext(childCtx, program, args...)
	stdout, err := command.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	if err := command.Start(); err != nil {
		cancel()
		return nil, err
	}
	time.Sleep(300 * time.Millisecond)
	if err := command.Process.Signal(nil); err != nil {
		cancel()
		_ = command.Wait()
		return nil, fmt.Errorf("%s exited before recording started", program)
	}
	handle := &processCapture{cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(handle.done)
		buffer := make([]byte, 2048)
		for {
			n, readErr := stdout.Read(buffer)
			if n > 0 {
				chunk := append([]byte(nil), buffer[:n]...)
				select {
				case output <- chunk:
				default:
				}
			}
			if readErr != nil {
				if readErr != io.EOF {
					cancel()
				}
				break
			}
		}
		_ = command.Wait()
	}()
	return handle, nil
}

func linuxRecorder(sampleRate int) (string, []string, error) {
	rate := fmt.Sprint(sampleRate)
	candidates := []struct {
		name string
		args []string
	}{
		{"pw-record", []string{"--rate", rate, "--channels", "1", "--format", "s16", "-"}},
		{"parec", []string{"--raw", "--format=s16le", "--rate=" + rate, "--channels=1"}},
		{"arecord", []string{"-q", "-t", "raw", "-f", "S16_LE", "-c", "1", "-r", rate, "-"}},
	}
	for _, candidate := range candidates {
		if path, err := exec.LookPath(candidate.name); err == nil {
			return path, candidate.args, nil
		}
	}
	return "", nil, errors.New("no microphone recorder found; install pw-record, parec, or arecord")
}

type processCapture struct {
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once
}

func (c *processCapture) Stop() {
	c.once.Do(func() {
		c.cancel()
		<-c.done
	})
}
