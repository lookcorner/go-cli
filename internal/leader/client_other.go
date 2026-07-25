//go:build !unix && !windows

package leader

import (
	"context"
	"errors"
	"io"
)

func dial(context.Context, string) (io.ReadWriteCloser, error) {
	return nil, errors.New("leader client IPC is not supported on this platform")
}

func connectOrSpawn(context.Context, string, Registration, SpawnFunc) (*Client, error) {
	return nil, errors.New("leader client IPC is not supported on this platform")
}
