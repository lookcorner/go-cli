package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lookcorner/go-cli/internal/api"
	"github.com/lookcorner/go-cli/internal/session"
)

func TestForeignResumeEventRequiresPristineStartup(t *testing.T) {
	recent := &session.RecentForeignSession{ForeignSummary: session.ForeignSummary{ID: "session-id", Source: "claude"}}
	for _, test := range []struct {
		name string
		m    *model
		want bool
	}{
		{name: "blank", m: &model{foreignResumeReady: true}, want: true},
		{name: "not ready", m: &model{}},
		{name: "input", m: &model{foreignResumeReady: true, input: []rune("draft")}},
		{name: "image", m: &model{foreignResumeReady: true, promptImages: make([]api.ContentPart, 1)}},
		{name: "running", m: &model{foreignResumeReady: true, running: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			updated, _ := test.m.Update(foreignResumeEvent{session: recent})
			if got := updated.(*model).foreignResume != nil; got != test.want {
				t.Fatalf("hint installed=%v", got)
			}
		})
	}

	withTranscript := &model{foreignResumeReady: true}
	withTranscript.transcript.WriteString("existing session")
	updated, _ := withTranscript.Update(foreignResumeEvent{session: recent})
	if updated.(*model).foreignResume != nil {
		t.Fatal("hint installed over existing transcript")
	}
}

func TestForeignResumeHintDismissesOnUserInteraction(t *testing.T) {
	m := &model{foreignResumeReady: true, status: "ready"}
	updated, _ := m.Update(tea.KeyPressMsg(tea.Key{Code: 'a', Text: "a"}))
	m = updated.(*model)
	if m.foreignResumeReady || m.foreignResume != nil || string(m.input) != "a" {
		t.Fatalf("ready=%v hint=%#v input=%q", m.foreignResumeReady, m.foreignResume, m.input)
	}
	updated, _ = m.Update(foreignResumeEvent{session: &session.RecentForeignSession{ForeignSummary: session.ForeignSummary{ID: "late", Source: "codex"}}})
	if updated.(*model).foreignResume != nil {
		t.Fatal("late result restored dismissed hint")
	}
}

func TestForeignResumeHintViewLabelsSourceAndAge(t *testing.T) {
	for _, test := range []struct {
		name   string
		source string
		age    time.Duration
		want   string
		when   string
	}{
		{name: "claude moments", source: "claude", want: "Coming from Claude Code?", when: "moments ago"},
		{name: "codex minutes", source: "codex", age: 2 * time.Minute, want: "Coming from Codex?", when: "2m ago"},
	} {
		t.Run(test.name, func(t *testing.T) {
			m := &model{
				width: 100, height: 12, status: "ready", modelName: "grok", workspace: "/work",
				foreignResume: &session.RecentForeignSession{ForeignSummary: session.ForeignSummary{ID: "id", Source: test.source}, Age: test.age},
			}
			content := stripUIANSI(m.View().Content)
			if !strings.Contains(content, test.want) || !strings.Contains(content, test.when) || !strings.Contains(content, "Ctrl-U") {
				t.Fatalf("content=%q", content)
			}
		})
	}
}

func TestForeignResumeCtrlUStartsFreshResumeSession(t *testing.T) {
	m := &model{
		foreignResumeReady: true,
		foreignResume:      &session.RecentForeignSession{ForeignSummary: session.ForeignSummary{ID: "abc-123", Source: "codex"}},
	}
	updated, command := m.handleKey(tea.KeyPressMsg(tea.Key{Code: 'u', Mod: tea.ModCtrl}))
	m = updated.(*model)
	if command == nil || !m.newSession || m.newSessionPrompt != "/resume-codex abc-123" || m.foreignResume != nil || m.foreignResumeReady {
		t.Fatalf("command=%v new=%v prompt=%q hint=%#v ready=%v", command != nil, m.newSession, m.newSessionPrompt, m.foreignResume, m.foreignResumeReady)
	}
	if _, ok := command().(tea.QuitMsg); !ok {
		t.Fatalf("command returned %T", command())
	}
}

func TestForeignResumeCtrlUStillClearsNormalInput(t *testing.T) {
	m := &model{foreignResumeReady: true, input: []rune("draft"), cursor: 5, status: "ready"}
	updated, command := m.handleKey(tea.KeyPressMsg(tea.Key{Code: 'u', Mod: tea.ModCtrl}))
	m = updated.(*model)
	if command != nil || len(m.input) != 0 || m.newSession || m.foreignResumeReady {
		t.Fatalf("command=%v input=%q new=%v ready=%v", command != nil, m.input, m.newSession, m.foreignResumeReady)
	}
}

func TestForeignResumeCtrlUIgnoresStaleHint(t *testing.T) {
	recent := &session.RecentForeignSession{ForeignSummary: session.ForeignSummary{ID: "abc-123", Source: "claude"}}
	for _, test := range []struct {
		name string
		m    *model
	}{
		{name: "image", m: &model{foreignResume: recent, foreignResumeReady: true, promptImages: make([]api.ContentPart, 1)}},
		{name: "transcript", m: func() *model {
			m := &model{foreignResume: recent, foreignResumeReady: true}
			m.transcript.WriteString("started")
			return m
		}()},
	} {
		t.Run(test.name, func(t *testing.T) {
			updated, command := test.m.handleKey(tea.KeyPressMsg(tea.Key{Code: 'u', Mod: tea.ModCtrl}))
			m := updated.(*model)
			if command != nil || m.newSession || m.foreignResume != nil || m.foreignResumeReady {
				t.Fatalf("command=%v new=%v hint=%#v ready=%v", command != nil, m.newSession, m.foreignResume, m.foreignResumeReady)
			}
		})
	}
}
