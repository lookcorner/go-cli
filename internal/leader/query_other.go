//go:build !unix && !windows

package leader

import (
	"context"
	"errors"
)

func query(context.Context, string) (QueryResult, error) {
	return QueryResult{}, errors.New("leader IPC is not supported on this platform")
}
