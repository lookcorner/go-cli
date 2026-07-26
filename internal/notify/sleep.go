package notify

import (
	"os/exec"
	"syscall"
)

// SleepInhibitor keeps the machine awake while an agent turn runs. Calls are
// idempotent, and a platform that cannot inhibit is only probed once.
type SleepInhibitor struct {
	enabled     bool
	start       func() (func(), error)
	stop        func()
	unavailable bool
}

// NewSleepInhibitor builds an inhibitor over the platform's idle-sleep command.
// A disabled inhibitor never spawns anything.
func NewSleepInhibitor(enabled bool) *SleepInhibitor {
	return &SleepInhibitor{enabled: enabled, start: startSleepCommand}
}

// Inhibit prevents idle sleep. It is a no-op when disabled, already inhibiting,
// or the platform command already failed once.
func (s *SleepInhibitor) Inhibit() {
	if s == nil || !s.enabled || s.stop != nil || s.unavailable || s.start == nil {
		return
	}
	stop, err := s.start()
	if err != nil || stop == nil {
		s.unavailable = true
		return
	}
	s.stop = stop
}

// Release allows idle sleep again. It is a no-op when not inhibiting.
func (s *SleepInhibitor) Release() {
	if s == nil || s.stop == nil {
		return
	}
	stop := s.stop
	s.stop = nil
	stop()
}

// Active reports whether idle sleep is currently inhibited.
func (s *SleepInhibitor) Active() bool { return s != nil && s.stop != nil }

// startSleepCommand spawns the platform's idle-sleep inhibitor and returns the
// function that terminates it.
func startSleepCommand() (func(), error) {
	argv := sleepCommand()
	if len(argv) == 0 {
		return nil, exec.ErrNotFound
	}
	command := exec.Command(argv[0], argv[1:]...)
	if err := command.Start(); err != nil {
		return nil, err
	}
	return func() {
		if command.Process != nil {
			_ = command.Process.Signal(syscall.SIGTERM)
		}
		_ = command.Wait()
	}, nil
}
