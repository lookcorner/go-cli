package leader

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

type KillAction string

const (
	KillTerminated KillAction = "terminated"
	KillCleaned    KillAction = "cleaned"
	KillFailed     KillAction = "failed"
)

type KillOutcome struct {
	PID    uint32
	Action KillAction
	Error  error
}

type KillResult struct {
	Outcomes []KillOutcome
	Killed   int
	Cleaned  int
}

func Kill(descriptors []Descriptor) KillResult {
	return kill(descriptors, isGorkProcess, terminateProcess, os.Remove)
}

func isGorkExecutable(path string) bool {
	name := strings.ToLower(filepath.Base(path))
	return strings.Contains(name, "gork") || strings.Contains(name, "go-cli")
}

func kill(
	descriptors []Descriptor,
	isGork func(uint32) bool,
	terminate func(uint32) error,
	remove func(string) error,
) KillResult {
	var result KillResult
	for _, descriptor := range descriptors {
		pid := descriptor.PID()
		if pid == nil {
			continue
		}
		if !isGork(*pid) {
			cleaned := false
			for _, path := range []string{descriptor.LockPath, descriptor.SocketPath} {
				if path == "" {
					continue
				}
				if err := remove(path); err == nil || errors.Is(err, os.ErrNotExist) {
					cleaned = true
				}
			}
			if cleaned {
				result.Cleaned++
				result.Outcomes = append(result.Outcomes, KillOutcome{PID: *pid, Action: KillCleaned})
			}
			continue
		}
		if err := terminate(*pid); err != nil {
			result.Outcomes = append(result.Outcomes, KillOutcome{PID: *pid, Action: KillFailed, Error: err})
			continue
		}
		result.Killed++
		result.Outcomes = append(result.Outcomes, KillOutcome{PID: *pid, Action: KillTerminated})
	}
	return result
}
