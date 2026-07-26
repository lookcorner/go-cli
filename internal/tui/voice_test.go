package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/voice"
)

type fakeVoiceStarter struct {
	session voice.Session
	err     error
}

func (f fakeVoiceStarter) Start(context.Context) (voice.Session, error) {
	return f.session, f.err
}

type fakeVoiceSession struct {
	events  chan voice.Event
	stopped int
}

func (s *fakeVoiceSession) Events() <-chan voice.Event { return s.events }
func (s *fakeVoiceSession) Stop()                      { s.stopped++ }

func TestVoiceDictationUpdatesPromptAndStops(t *testing.T) {
	session := &fakeVoiceSession{events: make(chan voice.Event, 2)}
	m := &model{ctx: context.Background(), runner: &agent.Runner{}, width: 80, height: 20, status: "ready"}
	m.voiceClient = fakeVoiceStarter{session: session}

	command := m.toggleVoice()
	if command == nil || !m.voiceStarting {
		t.Fatal("voice start command was not created")
	}
	updated, wait := m.Update(command())
	m = updated.(*model)
	if wait == nil || m.voiceSession != session || m.status == "" {
		t.Fatalf("voice did not start: session=%v status=%q", m.voiceSession, m.status)
	}

	session.events <- voice.Event{Text: "hello"}
	updated, wait = m.Update(wait())
	m = updated.(*model)
	if m.voiceInterim != "hello" || wait == nil {
		t.Fatalf("interim transcript not shown: %q", m.voiceInterim)
	}

	m.setInput("prefix")
	session.events <- voice.Event{Text: "world", Final: true}
	updated, wait = m.Update(wait())
	m = updated.(*model)
	if got := string(m.input); got != "prefix world" {
		t.Fatalf("dictation = %q", got)
	}
	if m.voiceInterim != "" || wait == nil {
		t.Fatal("final transcript did not clear interim preview")
	}

	updated, _ = m.handleKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if session.stopped != 1 || m.voiceSession != nil {
		t.Fatalf("voice stop failed: stopped=%d status=%q", session.stopped, m.status)
	}
}

func TestVoiceEnterCommitsInterimBeforeSubmit(t *testing.T) {
	tests := []struct {
		name    string
		draft   string
		interim string
		want    string
	}{
		{name: "draft", draft: "hello", interim: "world", want: "hello world"},
		{name: "interim only", interim: "ghost only", want: "ghost only"},
		{name: "blank draft", draft: "  \n", interim: "spoken", want: "spoken"},
		{name: "trailing whitespace", draft: "hello\n", interim: "world", want: "hello\nworld"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			session := &fakeVoiceSession{events: make(chan voice.Event)}
			m := &model{ctx: context.Background(), runner: &agent.Runner{}, voiceSession: session, voiceInterim: test.interim}
			m.setInput(test.draft)

			updated, _ := m.handleKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
			m = updated.(*model)

			if got := string(m.input); got != "" {
				t.Fatalf("input after submit = %q, want cleared", got)
			}
			if !strings.Contains(m.transcript.String(), test.want) {
				t.Fatalf("submitted transcript = %q, want %q", m.transcript.String(), test.want)
			}
			if session.stopped != 1 || m.voiceSession != nil || m.voiceInterim != "" {
				t.Fatalf("voice state after submit: stopped=%d session=%v interim=%q", session.stopped, m.voiceSession, m.voiceInterim)
			}
		})
	}
}

func TestVoiceEnterIgnoresLateFinal(t *testing.T) {
	session := &fakeVoiceSession{events: make(chan voice.Event)}
	m := &model{ctx: context.Background(), runner: &agent.Runner{}, voiceSession: session, voiceInterim: "world"}
	m.setInput("hello")

	updated, _ := m.handleKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	updated, _ = m.Update(voiceEvent{event: voice.Event{Text: "world", Final: true}, ok: true})
	m = updated.(*model)

	if got := m.transcript.String(); strings.Count(got, "hello world") != 1 {
		t.Fatalf("late final changed submitted prompt: %q", got)
	}
}

func TestVoiceFinalAppendsAtEndAndPreservesCursor(t *testing.T) {
	tests := []struct {
		name       string
		draft      string
		cursor     int
		want       string
		wantCursor int
	}{
		{name: "cursor at end follows", draft: "hello", cursor: 5, want: "hello world", wantCursor: 11},
		{name: "mid text cursor stays", draft: "hello again", cursor: 5, want: "hello again world", wantCursor: 5},
		{name: "blank draft replaced", draft: " \n", cursor: 0, want: "world", wantCursor: 5},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			m := &model{input: []rune(test.draft), cursor: test.cursor}
			m.insertDictation("world")
			if got := string(m.input); got != test.want || m.cursor != test.wantCursor {
				t.Fatalf("dictation = %q cursor=%d, want %q cursor=%d", got, m.cursor, test.want, test.wantCursor)
			}
		})
	}
}

func TestVoiceEscapeStopsWithoutSubmitting(t *testing.T) {
	session := &fakeVoiceSession{events: make(chan voice.Event)}
	m := &model{ctx: context.Background(), runner: &agent.Runner{}, voiceSession: session, voiceInterim: "discard me"}
	m.setInput("draft")
	updated, command := m.handleKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEsc}))
	m = updated.(*model)
	if command != nil || session.stopped != 1 || m.voiceInterim != "" || string(m.input) != "draft" {
		t.Fatalf("Esc stop = command:%v stopped:%d interim:%q input:%q", command != nil, session.stopped, m.voiceInterim, string(m.input))
	}
}

func TestVoiceHoldModeStartsOnPressAndStopsOnRelease(t *testing.T) {
	session := &fakeVoiceSession{events: make(chan voice.Event)}
	m := &model{
		ctx: context.Background(), runner: &agent.Runner{},
		voiceClient: fakeVoiceStarter{session: session}, voiceCaptureMode: "hold",
		voiceKeybindEnabled: true, voiceKeyReleases: true,
	}
	updated, command := m.handleKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyF8}))
	m = updated.(*model)
	if command == nil || !m.voiceStarting || !m.voiceHoldOwned {
		t.Fatalf("press command=%v starting=%v owned=%v", command != nil, m.voiceStarting, m.voiceHoldOwned)
	}
	updated, wait := m.Update(command())
	m = updated.(*model)
	if wait == nil || m.voiceSession != session {
		t.Fatal("voice session did not start")
	}
	updated, command = m.Update(tea.KeyReleaseMsg(tea.Key{Code: tea.KeyF8}))
	m = updated.(*model)
	if command != nil || session.stopped != 1 || m.voiceHoldOwned || m.status != "finishing voice input" {
		t.Fatalf("release command=%v stopped=%d owned=%v status=%q", command != nil, session.stopped, m.voiceHoldOwned, m.status)
	}
}

func TestVoiceHoldModeFallsBackToToggleWithoutKeyReleases(t *testing.T) {
	session := &fakeVoiceSession{events: make(chan voice.Event)}
	m := &model{
		ctx: context.Background(), runner: &agent.Runner{},
		voiceClient: fakeVoiceStarter{session: session}, voiceCaptureMode: "hold", voiceKeybindEnabled: true,
	}
	updated, command := m.handleKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyF8}))
	m = updated.(*model)
	updated, _ = m.Update(command())
	m = updated.(*model)
	updated, command = m.handleKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyF8}))
	m = updated.(*model)
	if command != nil || session.stopped != 1 || m.voiceHoldOwned {
		t.Fatalf("fallback command=%v stopped=%d owned=%v", command != nil, session.stopped, m.voiceHoldOwned)
	}
}

func TestVoiceShortcutCanBeDisabledWithoutDisablingVoiceCommand(t *testing.T) {
	session := &fakeVoiceSession{events: make(chan voice.Event)}
	m := &model{
		ctx: context.Background(), runner: &agent.Runner{},
		voiceClient: fakeVoiceStarter{session: session}, voiceCaptureMode: "toggle",
		width: 80, height: 20,
	}
	updated, command := m.handleKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyF8}))
	m = updated.(*model)
	if command != nil || m.voiceStarting || m.voiceHoldOwned {
		t.Fatalf("disabled shortcut command=%v starting=%v owned=%v", command != nil, m.voiceStarting, m.voiceHoldOwned)
	}

	m.setInput("/voice")
	updated, command = m.handleKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command == nil || !m.voiceStarting {
		t.Fatalf("/voice command=%v starting=%v status=%q", command != nil, m.voiceStarting, m.status)
	}
	if view := stripUIANSI(m.View().Content); !strings.Contains(view, "/voice cancel") || strings.Contains(view, "Ctrl-Space/F8 cancel") {
		t.Fatalf("disabled shortcut hint=%q", view)
	}
}

func TestVoiceCommandIsOnlySuggestedWhenAvailable(t *testing.T) {
	m := &model{ctx: context.Background(), runner: &agent.Runner{}, width: 80, height: 20, status: "ready"}
	m.setInput("/voi")
	if suggestions := m.slashSuggestions(); len(suggestions) != 0 {
		t.Fatalf("voice should be hidden without a client: %#v", suggestions)
	}
	m.voiceClient = fakeVoiceStarter{}
	suggestions := m.slashSuggestions()
	if len(suggestions) != 1 || suggestions[0].insert != "/voice" {
		t.Fatalf("voice suggestion missing: %#v", suggestions)
	}
}
