//go:build windows

package wrap

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/UserExistsError/conpty"
	"github.com/charmbracelet/x/term"
)

func PTYSupported() bool { return conpty.IsConPtyAvailable() }

// conptyDrainGrace bounds how long RunPTY waits for ConPTY teardown output
// after the wrapped process exits; unlike a Unix PTY master, the ConPTY
// output pipe can stay open well past process exit while conhost lingers.
const conptyDrainGrace = 3 * time.Second

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

	if resolved, lookErr := exec.LookPath(program); lookErr == nil {
		program = resolved
	}
	master, err := conpty.Start(
		joinCommandLine(program, args),
		conpty.ConPtyDimensions(width, height),
		conpty.ConPtyEnv(append(os.Environ(), "GROK_OSC52_SINK=1", "LC_GROK_OSC52_SINK=1")),
	)
	if err != nil {
		return 0, err
	}
	var closeOnce sync.Once
	closeMaster := func() { closeOnce.Do(func() { _ = master.Close() }) }
	defer closeMaster()

	resizeDone := make(chan struct{})
	defer close(resizeDone)
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				w, h, sizeErr := term.GetSize(stdout.Fd())
				if sizeErr == nil && (w != width || h != height) {
					width, height = w, h
					_ = master.Resize(w, h)
				}
			case <-resizeDone:
				return
			}
		}
	}()
	var masterMu sync.Mutex
	writeMaster := func(p []byte) (int, error) {
		masterMu.Lock()
		defer masterMu.Unlock()
		return master.Write(p)
	}
	go func() {
		buffer := make([]byte, 8192)
		for {
			count, err := stdin.Read(buffer)
			if count > 0 {
				if _, writeErr := writeMaster(buffer[:count]); writeErr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	filter := NewFilter(func(data []byte) {
		if copyText == nil || !utf8.Valid(data) {
			return
		}
		if err := copyText(string(data)); err != nil {
			fmt.Fprintln(stderr, "gork wrap: clipboard copy failed:", err)
		}
	})
	filter.SetImageRequestHandler(func() {
		go func() {
			frame := HostClipboardImageFrame(nil)
			frame = append(frame, '\n')
			_, _ = writeMaster(frame)
		}()
	})
	defer func() {
		if restore := filter.EmitRestore(); len(restore) > 0 {
			_, _ = stdout.Write(restore)
		}
	}()
	readErr := make(chan error, 1)
	go func() {
		buffer := make([]byte, 8192)
		for {
			count, err := master.Read(buffer)
			if count > 0 {
				if _, err := stdout.Write(filter.Feed(buffer[:count])); err != nil {
					readErr <- err
					return
				}
			}
			if err != nil {
				if errors.Is(err, io.EOF) || errors.Is(err, syscall.ERROR_BROKEN_PIPE) {
					readErr <- nil
				} else {
					readErr <- err
				}
				return
			}
		}
	}()

	code, err := master.Wait(context.Background())
	if err != nil {
		return 0, err
	}
	timer := time.NewTimer(conptyDrainGrace)
	select {
	case err := <-readErr:
		timer.Stop()
		if err != nil {
			return 0, err
		}
	case <-timer.C:
		closeMaster()
		<-readErr
	}
	if remaining := filter.Flush(); len(remaining) > 0 {
		if _, err := stdout.Write(remaining); err != nil {
			return 0, err
		}
	}
	return int(code), nil
}

func joinCommandLine(program string, args []string) string {
	quoted := make([]string, 0, len(args)+1)
	quoted = append(quoted, escapeArg(program))
	for _, arg := range args {
		quoted = append(quoted, escapeArg(arg))
	}
	return strings.Join(quoted, " ")
}

// escapeArg quotes one argument following CommandLineToArgvW rules.
func escapeArg(arg string) string {
	if arg != "" && !strings.ContainsAny(arg, " \t\"") {
		return arg
	}
	var b strings.Builder
	b.WriteByte('"')
	backslashes := 0
	for _, r := range arg {
		switch r {
		case '\\':
			backslashes++
		case '"':
			b.WriteString(strings.Repeat(`\`, backslashes*2+1))
			b.WriteByte('"')
			backslashes = 0
		default:
			b.WriteString(strings.Repeat(`\`, backslashes))
			backslashes = 0
			b.WriteRune(r)
		}
	}
	b.WriteString(strings.Repeat(`\`, backslashes*2))
	b.WriteByte('"')
	return b.String()
}
