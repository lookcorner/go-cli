//go:build windows

package leader

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/Microsoft/go-winio"
)

func dial(ctx context.Context, socketPath string) (io.ReadWriteCloser, error) {
	return winio.DialPipeContext(ctx, pipeName(socketPath))
}

func connectOrSpawn(ctx context.Context, socketPath string, registration Registration, spawn SpawnFunc) (*Client, error) {
	if client, err := Connect(ctx, socketPath, registration); err == nil {
		return client, nil
	}
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o700); err != nil {
		return nil, err
	}
	for {
		lock, err := openLeaderLock(socketPath + ".spawn")
		if err == nil {
			client, connectErr := Connect(ctx, socketPath, registration)
			if connectErr == nil {
				_ = lock.Close()
				return client, nil
			}
			_ = os.Remove(socketPath)
			if err := spawn(); err != nil {
				_ = lock.Close()
				return nil, err
			}
			client, err = waitForLeader(ctx, socketPath, registration)
			_ = lock.Close()
			return client, err
		}
		if !isLockHeld(err) {
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
