//go:build !unix

package mcp

import "context"

func acquireMCPAuthFileLock(context.Context, string) (func(), error) {
	return nil, nil
}
