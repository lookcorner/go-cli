//go:build windows

package leader

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/Microsoft/go-winio"
)

func (s *Server) startPlatform() (returnErr error) {
	socketPath, lockPath := s.paths()
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o700); err != nil {
		return err
	}
	lock, err := openLeaderLock(lockPath)
	if err != nil {
		if isLockHeld(err) {
			return fmt.Errorf("leader is already running: %w", err)
		}
		return err
	}
	defer func() {
		if returnErr != nil {
			_ = lock.Close()
			_ = os.Remove(socketPath)
			_ = os.Remove(lockPath)
		}
	}()
	if err := lock.file.Truncate(0); err != nil {
		return err
	}
	if _, err := lock.file.WriteString(strconv.Itoa(os.Getpid())); err != nil {
		return err
	}
	if err := lock.file.Sync(); err != nil {
		return err
	}
	_ = os.Remove(socketPath)
	if err := os.WriteFile(socketPath, []byte(pipeName(socketPath)), 0o600); err != nil {
		return err
	}
	// Zero buffer sizes produce a zero-quota pipe on some Windows builds,
	// where every write pends until the reader drains; size them explicitly.
	listener, err := winio.ListenPipe(pipeName(socketPath), &winio.PipeConfig{
		InputBufferSize: 64 << 10, OutputBufferSize: 64 << 10,
	})
	if err != nil {
		return err
	}
	s.listener = listener
	s.lock = lock
	s.info = Info{
		PID: uint32(os.Getpid()), SocketPath: socketPath, LockPath: lockPath,
		LeaderProtocolVersion: ProtocolVersion, LeaderBinaryVersion: s.config.BinaryVersion,
		ProfileFormats: []string{},
	}
	return nil
}

func (s *Server) cleanupPlatform() error {
	var err error
	if s.lock != nil {
		// Close the lock before removing paths; Windows rejects deleting
		// open files.
		err = s.lock.Close()
	}
	return errors.Join(err, removeLeaderPath(s.info.SocketPath), removeLeaderPath(s.info.LockPath))
}
