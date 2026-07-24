//go:build unix

package leader

import (
	"context"
	"net"
)

func query(ctx context.Context, socketPath string) (QueryResult, error) {
	dialer := net.Dialer{}
	connection, err := dialer.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return QueryResult{}, err
	}
	return queryStream(ctx, connection)
}
