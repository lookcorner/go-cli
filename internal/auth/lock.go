package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const staleLockAge = 2 * time.Minute

type fileLock struct {
	path  string
	token string
}

func acquireFileLock(ctx context.Context, authPath string) (*fileLock, error) {
	if err := os.MkdirAll(filepath.Dir(authPath), 0o700); err != nil {
		return nil, err
	}
	lockPath := filepath.Join(filepath.Dir(authPath), "auth.json.lock")
	tokenBytes := make([]byte, 16)
	if _, err := rand.Read(tokenBytes); err != nil {
		return nil, err
	}
	token := fmt.Sprintf("%d:%s", os.Getpid(), hex.EncodeToString(tokenBytes))
	for {
		file, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			if _, err = file.WriteString(token); err == nil {
				err = file.Close()
			} else {
				file.Close()
			}
			if err != nil {
				_ = os.Remove(lockPath)
				return nil, err
			}
			return &fileLock{path: lockPath, token: token}, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		if info, statErr := os.Stat(lockPath); statErr == nil && time.Since(info.ModTime()) > staleLockAge {
			_ = os.Remove(lockPath)
			continue
		}
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func (l *fileLock) release() {
	if l == nil {
		return
	}
	data, err := os.ReadFile(l.path)
	if err == nil && string(data) == l.token {
		_ = os.Remove(l.path)
	}
}
