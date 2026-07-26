package notify

import (
	"os/exec"
	"slices"
	"strings"
	"time"
)

// Hook is one [[ui.notifications.hooks]] entry: a shell command run alongside
// terminal notifications. An empty event list matches every event.
type Hook struct {
	Command       string
	Events        []Event
	OnlyUnfocused bool
	Timeout       time.Duration
}

// Matches reports whether a hook fires for an event at the current focus state.
func (h Hook) Matches(event Event, focused bool) bool {
	if strings.TrimSpace(h.Command) == "" {
		return false
	}
	if h.OnlyUnfocused && focused {
		return false
	}
	return len(h.Events) == 0 || slices.Contains(h.Events, event)
}

// RunHooks starts every matching hook in the background. Hooks are fire and
// forget: spawn failures, non-zero exits, and timeouts never reach the session.
func RunHooks(hooks []Hook, event Event, sessionID string, focused bool) {
	for _, hook := range hooks {
		if hook.Matches(event, focused) {
			go hook.Run(event, sessionID)
		}
	}
}

// Run executes one hook synchronously, killing its whole process group if it
// outlives the configured timeout.
func (h Hook) Run(event Event, sessionID string) {
	command := exec.Command("sh", "-c", h.Command)
	command.Env = append(command.Environ(), "GROK_EVENT="+event.Label(), "GROK_MESSAGE="+event.Label())
	if strings.TrimSpace(sessionID) != "" {
		command.Env = append(command.Env, "GROK_SESSION_ID="+sessionID)
	}
	detachProcessGroup(command)
	if err := command.Start(); err != nil {
		return
	}
	finished := make(chan struct{})
	go func() {
		_ = command.Wait()
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(max(h.Timeout, time.Second)):
		killProcessGroup(command.Process.Pid)
		<-finished
	}
}
