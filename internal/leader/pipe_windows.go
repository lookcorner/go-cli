//go:build windows

package leader

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// pipeName maps a leader socket marker path to a deterministic named-pipe
// address. Windows named pipes live in a flat machine-wide namespace, so the
// on-disk marker file keeps directory-based discovery working unchanged.
func pipeName(socketPath string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(filepath.Clean(socketPath))))
	return `\\.\pipe\gork-go-leader-` + hex.EncodeToString(sum[:8])
}

// leaderLockFile is a Windows file lock: opening with read-only sharing
// denies a second leader write access while leaving the PID readable.
type leaderLockFile struct{ file *os.File }

func openLeaderLock(path string) (*leaderLockFile, error) {
	pathp, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(
		pathp, windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ, nil, windows.OPEN_ALWAYS,
		windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, err
	}
	return &leaderLockFile{file: os.NewFile(uintptr(handle), path)}, nil
}

func (l *leaderLockFile) Close() error { return l.file.Close() }

func isLockHeld(err error) bool {
	return errors.Is(err, windows.ERROR_SHARING_VIOLATION) ||
		errors.Is(err, windows.ERROR_LOCK_VIOLATION)
}
