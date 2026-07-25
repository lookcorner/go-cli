//go:build windows

package leader

import (
	"context"

	"github.com/Microsoft/go-winio"
)

func query(ctx context.Context, socketPath string) (QueryResult, error) {
	connection, err := winio.DialPipeContext(ctx, pipeName(socketPath))
	if err != nil {
		return QueryResult{}, err
	}
	return queryStream(ctx, connection)
}
