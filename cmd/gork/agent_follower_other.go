//go:build !unix && !windows

package main

import "errors"

func startAgentLeader(string, []string) error {
	return errors.New("leader follower mode is not supported on this platform")
}
