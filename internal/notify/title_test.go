package notify

import (
	"strings"
	"testing"
	"time"
)

// titleText strips the OSC 0 envelope so tests read the composed title.
func titleText(t *testing.T, escape string) string {
	t.Helper()
	if escape == "" {
		return ""
	}
	if !strings.HasPrefix(escape, "\x1b]0;") || !strings.HasSuffix(escape, "\x07") {
		t.Fatalf("malformed title escape %q", escape)
	}
	return strings.TrimSuffix(strings.TrimPrefix(escape, "\x1b]0;"), "\x07")
}

func TestTitleComposesConfiguredItemsInOrder(t *testing.T) {
	manager := NewTitleManager(true, []string{TitleActionRequired, TitleSpinner, TitleActivity, TitleSessionName, TitleGrok})
	state := TitleState{SessionName: "gork-go", Activity: "Thinking", Busy: true, PendingPermission: true, Focused: true}
	if got, want := titleText(t, manager.Update(state)), "⚠ Action Required - ⠋ - Thinking - gork-go - gork"; got != want {
		t.Fatalf("title=%q want %q", got, want)
	}

	idle := NewTitleManager(true, []string{TitleSpinner, TitleActivity, TitleTurnTimer, TitleSessionName})
	if got, want := titleText(t, idle.Update(TitleState{SessionName: "gork-go"})), "gork-go"; got != want {
		t.Fatalf("idle title=%q want %q", got, want)
	}
	if got, want := titleText(t, idle.Update(TitleState{Busy: true})), "⠙ - Waiting"; got != want {
		t.Fatalf("busy without activity=%q want %q", got, want)
	}

	empty := NewTitleManager(true, []string{TitleSessionName, TitleModel, TitleCwd})
	if got, want := titleText(t, empty.Update(TitleState{})), "gork"; got != want {
		t.Fatalf("empty title=%q want %q", got, want)
	}
}

func TestTitleRendersEachItemValue(t *testing.T) {
	for _, test := range []struct {
		item  string
		state TitleState
		want  string
	}{
		{TitleGrok, TitleState{}, "gork"},
		{TitleModel, TitleState{Model: "grok-4"}, "grok-4"},
		{TitleModel, TitleState{Model: strings.Repeat("m", 31)}, strings.Repeat("m", 30) + "…"},
		{TitleSessionName, TitleState{SessionName: strings.Repeat("s", 41)}, strings.Repeat("s", 40) + "…"},
		{TitleCwd, TitleState{Cwd: "/home/user/projects/gork-go"}, "gork-go"},
		{TitleCwd, TitleState{Cwd: "gork-go"}, "gork-go"},
		{TitleCwd, TitleState{}, "gork"},
		{TitleTurnTimer, TitleState{TurnElapsed: 900 * time.Millisecond}, "gork"},
		{TitleTurnTimer, TitleState{TurnElapsed: 90 * time.Second}, "90s"},
		{TitleActivity, TitleState{Activity: "Responding"}, "Responding"},
		{TitleActivity, TitleState{}, "gork"},
		{TitleActionRequired, TitleState{}, "gork"},
		{TitleActionRequired, TitleState{PendingPermission: true, Focused: true}, "⚠ Action Required"},
	} {
		manager := NewTitleManager(true, []string{test.item})
		if got := titleText(t, manager.Update(test.state)); got != test.want {
			t.Errorf("%s: title=%q want %q", test.item, got, test.want)
		}
	}
}

func TestTitleAnimatesSpinnerAndActionBlink(t *testing.T) {
	spinner := NewTitleManager(true, []string{TitleSpinner})
	frames := make([]string, 0, len(titleSpinner)+1)
	for range len(titleSpinner) + 1 {
		frames = append(frames, titleText(t, spinner.Update(TitleState{Busy: true})))
	}
	if frames[0] != "⠋" || frames[1] != "⠙" || frames[len(titleSpinner)-1] != "⠧" || frames[len(titleSpinner)] != "⠋" {
		t.Fatalf("spinner frames=%q", frames)
	}

	blink := NewTitleManager(true, []string{TitleActionRequired, TitleGrok})
	away := TitleState{PendingPermission: true}
	visible := make([]bool, 0, 6)
	for range 6 {
		title := titleText(t, blink.Update(away))
		if title == "" {
			visible = append(visible, visible[len(visible)-1])
			continue
		}
		visible = append(visible, strings.Contains(title, titleActionLabel))
	}
	if visible[0] != true || visible[1] != true || visible[2] != false || visible[3] != false || visible[4] != true {
		t.Fatalf("blink phases=%v", visible)
	}

	focused := NewTitleManager(true, []string{TitleActionRequired})
	state := TitleState{PendingPermission: true, Focused: true}
	first := titleText(t, focused.Update(state))
	for range 6 {
		if got := focused.Update(state); got != "" {
			t.Fatalf("focused title flickered: %q", titleText(t, got))
		}
	}
	if first != titleActionLabel {
		t.Fatalf("focused first title=%q", first)
	}
}

func TestTitleDedupesResetsAndSanitizes(t *testing.T) {
	manager := NewTitleManager(true, []string{TitleSessionName})
	if got := titleText(t, manager.Update(TitleState{SessionName: "one"})); got != "one" {
		t.Fatalf("first=%q", got)
	}
	if got := manager.Update(TitleState{SessionName: "one"}); got != "" {
		t.Fatalf("unchanged title re-emitted %q", got)
	}
	if got := titleText(t, manager.Update(TitleState{SessionName: "two"})); got != "two" {
		t.Fatalf("changed=%q", got)
	}
	if got := titleText(t, manager.Reset()); got != "gork" {
		t.Fatalf("reset=%q", got)
	}

	injected := NewTitleManager(true, []string{TitleSessionName})
	if got, want := titleText(t, injected.Update(TitleState{SessionName: "a\x07b\x1b]0;evil"})), "ab]0;evil"; got != want {
		t.Fatalf("sanitized=%q want %q", got, want)
	}

	disabled := NewTitleManager(false, []string{TitleGrok})
	if disabled.Update(TitleState{Busy: true}) != "" || disabled.Reset() != "" || disabled.Animating(TitleState{Busy: true}) {
		t.Fatal("disabled manager produced output")
	}
	var absent *TitleManager
	if absent.Update(TitleState{}) != "" || absent.Reset() != "" || absent.Animating(TitleState{Busy: true}) {
		t.Fatal("nil manager produced output")
	}
}

func TestTitleAnimatingFollowsConfiguredItems(t *testing.T) {
	for _, test := range []struct {
		name  string
		items []string
		state TitleState
		want  bool
	}{
		{"spinner while busy", []string{TitleSpinner}, TitleState{Busy: true}, true},
		{"spinner while idle", []string{TitleSpinner}, TitleState{}, false},
		{"timer while busy", []string{TitleTurnTimer}, TitleState{Busy: true}, true},
		{"blink while away", []string{TitleActionRequired}, TitleState{PendingPermission: true}, true},
		{"no blink while focused", []string{TitleActionRequired}, TitleState{PendingPermission: true, Focused: true}, false},
		{"static items", []string{TitleGrok, TitleSessionName, TitleCwd, TitleModel}, TitleState{Busy: true, PendingPermission: true}, false},
	} {
		manager := NewTitleManager(true, test.items)
		if got := manager.Animating(test.state); got != test.want {
			t.Errorf("%s: animating=%v want %v", test.name, got, test.want)
		}
	}
}
