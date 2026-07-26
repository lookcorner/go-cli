package notify

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHookMatchesEventsAndFocus(t *testing.T) {
	for _, test := range []struct {
		name    string
		hook    Hook
		event   Event
		focused bool
		want    bool
	}{
		{"empty list matches all", Hook{Command: "x"}, AgentError, false, true},
		{"listed event", Hook{Command: "x", Events: []Event{TurnComplete}}, TurnComplete, false, true},
		{"unlisted event", Hook{Command: "x", Events: []Event{TurnComplete}}, AgentError, false, false},
		{"only unfocused while focused", Hook{Command: "x", OnlyUnfocused: true}, TurnComplete, true, false},
		{"only unfocused while away", Hook{Command: "x", OnlyUnfocused: true}, TurnComplete, false, true},
		{"focus ignored", Hook{Command: "x"}, TurnComplete, true, true},
		{"blank command", Hook{Command: "  "}, TurnComplete, false, false},
	} {
		if got := test.hook.Matches(test.event, test.focused); got != test.want {
			t.Errorf("%s: matches=%v want %v", test.name, got, test.want)
		}
	}
}

func TestHookRunExportsEventEnvironment(t *testing.T) {
	output := filepath.Join(t.TempDir(), "hook.txt")
	hook := Hook{Command: "printf '%s|%s|%s' \"$GROK_EVENT\" \"$GROK_MESSAGE\" \"$GROK_SESSION_ID\" > " + output}
	hook.Run(AgentError, "session-7")

	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(data), "Agent error|Agent error|session-7"; got != want {
		t.Fatalf("hook output=%q want %q", got, want)
	}

	blank := filepath.Join(t.TempDir(), "blank.txt")
	Hook{Command: "printf '[%s]' \"$GROK_SESSION_ID\" > " + blank}.Run(TurnComplete, "  ")
	if data, err = os.ReadFile(blank); err != nil || string(data) != "[]" {
		t.Fatalf("blank session id=%q err=%v", data, err)
	}
}

func TestHookRunKillsOverrunningProcessTree(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "late.txt")
	hook := Hook{Command: "(sleep 30; touch " + marker + ") & wait", Timeout: time.Second}

	start := time.Now()
	hook.Run(TurnComplete, "")
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("hook was not killed after its timeout: %s", elapsed)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("killed hook still produced its delayed effect")
	}
}

func TestRunHooksDispatchesOnlyMatchingHooks(t *testing.T) {
	dir := t.TempDir()
	path := func(name string) string { return filepath.Join(dir, name) }
	hooks := []Hook{
		{Command: "touch " + path("all")},
		{Command: "touch " + path("listed"), Events: []Event{TurnComplete}},
		{Command: "touch " + path("other"), Events: []Event{SessionReady}},
		{Command: "touch " + path("focused"), OnlyUnfocused: true},
	}
	RunHooks(hooks, TurnComplete, "", true)

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path("all")); err == nil {
			if _, err = os.Stat(path("listed")); err == nil {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	var missing, extra []string
	for _, name := range []string{"all", "listed"} {
		if _, err := os.Stat(path(name)); err != nil {
			missing = append(missing, name)
		}
	}
	for _, name := range []string{"other", "focused"} {
		if _, err := os.Stat(path(name)); err == nil {
			extra = append(extra, name)
		}
	}
	if len(missing) > 0 || len(extra) > 0 {
		t.Fatalf("missing=%s unexpected=%s", strings.Join(missing, ","), strings.Join(extra, ","))
	}
}
