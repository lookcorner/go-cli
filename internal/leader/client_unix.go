//go:build unix

package leader

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

func dial(ctx context.Context, socketPath string) (io.ReadWriteCloser, error) {
	return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
}

func connectOrSpawn(ctx context.Context, socketPath string, registration Registration, spawn SpawnFunc) (*Client, error) {
	if client, err := Connect(ctx, socketPath, registration); err == nil {
		return client, nil
	}
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o700); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(socketPath+".spawn", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	defer lock.Close()

	for {
		if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err == nil {
			client, connectErr := Connect(ctx, socketPath, registration)
			if connectErr == nil {
				_ = unix.Flock(int(lock.Fd()), unix.LOCK_UN)
				return client, nil
			}
			_ = os.Remove(socketPath)
			if err := spawn(); err != nil {
				_ = unix.Flock(int(lock.Fd()), unix.LOCK_UN)
				return nil, err
			}
			client, err = waitForLeader(ctx, socketPath, registration)
			_ = unix.Flock(int(lock.Fd()), unix.LOCK_UN)
			return client, err
		} else if !errors.Is(err, unix.EWOULDBLOCK) {
			return nil, err
		}
		attempt, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		client, err := waitForLeader(attempt, socketPath, registration)
		cancel()
		if err == nil {
			return client, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
}

func waitForLeader(ctx context.Context, socketPath string, registration Registration) (*Client, error) {
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		attempt, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
		client, err := Connect(attempt, socketPath, registration)
		cancel()
		if err == nil {
			return client, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}
