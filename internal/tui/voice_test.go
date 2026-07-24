package tui

import (
	"context"
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
	if session.stopped != 1 || !m.voiceSendOnStop || m.status != "finishing voice input" {
		t.Fatalf("voice stop failed: stopped=%d status=%q", session.stopped, m.status)
	}
	close(session.events)
	updated, submit := m.Update(wait())
	m = updated.(*model)
	if submit == nil || m.voiceSession != nil || m.voiceSendOnStop {
		t.Fatal("voice Enter did not wait for the final transcript before submitting")
	}
	if key, ok := submit().(tea.KeyPressMsg); !ok || key.Key().Code != tea.KeyEnter {
		t.Fatalf("submit message = %#v", key)
	}
}

func TestVoiceEscapeStopsWithoutSubmitting(t *testing.T) {
	session := &fakeVoiceSession{events: make(chan voice.Event)}
	m := &model{ctx: context.Background(), runner: &agent.Runner{}, voiceSession: session}
	updated, command := m.handleKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEsc}))
	m = updated.(*model)
	if command != nil || session.stopped != 1 || m.voiceSendOnStop {
		t.Fatalf("Esc stop = command:%v stopped:%d send:%v", command != nil, session.stopped, m.voiceSendOnStop)
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
