//go:build unix

package leader

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"

	"golang.org/x/sys/unix"
)

type leaderLockFile struct {
	file *os.File
}

func (l *leaderLockFile) Close() error {
	return errors.Join(unix.Flock(int(l.file.Fd()), unix.LOCK_UN), l.file.Close())
}

func (s *Server) startPlatform() (returnErr error) {
	socketPath, lockPath := s.paths()
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o700); err != nil {
		return err
	}
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	if err := unix.Flock(int(lockFile.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = lockFile.Close()
		return fmt.Errorf("leader is already running: %w", err)
	}
	defer func() {
		if returnErr != nil {
			_ = unix.Flock(int(lockFile.Fd()), unix.LOCK_UN)
			_ = lockFile.Close()
			_ = os.Remove(socketPath)
			_ = os.Remove(lockPath)
		}
	}()
	if err := lockFile.Truncate(0); err != nil {
		return err
	}
	if _, err := lockFile.WriteString(strconv.Itoa(os.Getpid())); err != nil {
		return err
	}
	if err := lockFile.Sync(); err != nil {
		return err
	}
	_ = os.Remove(socketPath)
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return err
	}
	if err := os.Chmod(socketPath, 0o600); err != nil {
		_ = listener.Close()
		return err
	}
	s.listener = listener
	s.lock = &leaderLockFile{file: lockFile}
	s.info = Info{
		PID: uint32(os.Getpid()), SocketPath: socketPath, LockPath: lockPath,
		LeaderProtocolVersion: ProtocolVersion, LeaderBinaryVersion: s.config.BinaryVersion,
		ProfileFormats: []string{},
	}
	return nil
}

func (s *Server) cleanupPlatform() error {
	err := errors.Join(removeLeaderPath(s.info.SocketPath), removeLeaderPath(s.info.LockPath))
	if s.lock != nil {
		err = errors.Join(err, s.lock.Close())
	}
	return err
}
