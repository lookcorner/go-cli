//go:build unix

package wrap

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"unicode/utf8"

	"github.com/charmbracelet/x/term"
	"github.com/creack/pty"
)

func PTYSupported() bool { return true }

func RunPTY(program string, args []string, stdin, stdout, stderr *os.File, copyText func(string) error) (int, error) {
	width, height, err := term.GetSize(stdout.Fd())
	if err != nil {
		width, height = 80, 24
	}
	oldState, err := term.MakeRaw(stdin.Fd())
	if err != nil {
		return 0, fmt.Errorf("enable raw terminal mode: %w", err)
	}
	defer term.Restore(stdin.Fd(), oldState)

	command := exec.Command(program, args...)
	command.Env = append(os.Environ(), "GROK_OSC52_SINK=1", "LC_GROK_OSC52_SINK=1")
	master, err := pty.StartWithSize(command, &pty.Winsize{Rows: uint16(height), Cols: uint16(width)})
	if err != nil {
		return 0, err
	}
	defer master.Close()

	resize := make(chan os.Signal, 1)
	resizeDone := make(chan struct{})
	signal.Notify(resize, syscall.SIGWINCH)
	defer func() {
		signal.Stop(resize)
		close(resizeDone)
	}()
	go func() {
		for {
			select {
			case <-resize:
				_ = pty.InheritSize(master, stdout)
			case <-resizeDone:
				return
			}
		}
	}()
	resize <- syscall.SIGWINCH
	go func() { _, _ = io.Copy(master, stdin) }()

	filter := NewFilter(func(data []byte) {
		if copyText == nil || !utf8.Valid(data) {
			return
		}
		if err := copyText(string(data)); err != nil {
			fmt.Fprintln(stderr, "gork wrap: clipboard copy failed:", err)
		}
	})
	buffer := make([]byte, 8192)
	for {
		count, readErr := master.Read(buffer)
		if count > 0 {
			if _, err := stdout.Write(filter.Feed(buffer[:count])); err != nil {
				return 0, err
			}
		}
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) && !errors.Is(readErr, syscall.EIO) {
				return 0, readErr
			}
			break
		}
	}
	if remaining := filter.Flush(); len(remaining) > 0 {
		if _, err := stdout.Write(remaining); err != nil {
			return 0, err
		}
	}
	err = command.Wait()
	if err == nil {
		return 0, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), nil
	}
	return 0, err
}
