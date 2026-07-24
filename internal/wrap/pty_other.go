//go:build !unix

package wrap

import (
	"errors"
	"os"
)

func PTYSupported() bool { return false }

func RunPTY(string, []string, *os.File, *os.File, *os.File, func(string) error) (int, error) {
	return 0, errors.New("PTY wrapping is not supported on this platform")
}
