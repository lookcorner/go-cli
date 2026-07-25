//go:build !unix && !windows

package leader

import "errors"

func (s *Server) startPlatform() error {
	return errors.New("leader server IPC is not supported on this platform")
}

func (s *Server) cleanupPlatform() error {
	return nil
}
