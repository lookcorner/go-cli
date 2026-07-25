package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

func TestWordSelectHintShowsOnSecondClickAndAccepts(t *testing.T) {
	var persisted string
	m := &model{
		width: 80, height: 30, status: "ready", selectionMode: selectionFlash,
		wordSelectHint: wordSelectHintState{enabled: true},
		persistSelection: func(mode string) error {
			persisted = mode
			return nil
		},
	}
	lines := []string{"assistant response"}
	at := time.Now()
	point := selectionPoint{line: 0, column: 3}
	updated, command := m.Update(mouseSelectionEvent{phase: selectionStart, point: point, lines: lines, at: at, assistant: true})
	m = updated.(*model)
	if command != nil || m.wordSelectHint.active {
		t.Fatalf("first click command=%v active=%v", command != nil, m.wordSelectHint.active)
	}
	updated, command = m.Update(mouseSelectionEvent{phase: selectionStart, point: point, lines: lines, at: at.Add(100 * time.Millisecond), assistant: true})
	m = updated.(*model)
	if command == nil || !m.wordSelectHint.active || m.wordSelectHint.shown != 1 ||
		m.wordSelectHint.remaining != 20*time.Second {
		t.Fatalf("second click command=%v hint=%#v", command != nil, m.wordSelectHint)
	}
	if content := stripUIANSI(m.View().Content); !strings.Contains(content, "Want double-click to select?") ||
		!strings.Contains(content, "Ctrl+Y: enable now") {
		t.Fatalf("content=%q", content)
	}

	updated, command = m.Update(tea.KeyPressMsg(tea.Key{Code: 'y', Mod: tea.ModCtrl}))
	m = updated.(*model)
	if command != nil || m.selectionMode != selectionWord || persisted != "word_select" ||
		m.wordSelectHint.active || m.status != "Text selection: word select" {
		t.Fatalf("command=%v mode=%q persisted=%q active=%v status=%q", command != nil, m.selectionMode.canonical(), persisted, m.wordSelectHint.active, m.status)
	}
}

func TestWordSelectHintIsGatedAndRetiresOnEdit(t *testing.T) {
	point := selectionPoint{line: 0, column: 1}
	lines := []string{"text"}
	at := time.Now()
	for _, test := range []struct {
		name string
		m    *model
	}{
		{name: "disabled", m: &model{wordSelectHint: wordSelectHintState{}, selectionMode: selectionFlash}},
		{name: "already word select", m: &model{wordSelectHint: wordSelectHintState{enabled: true}, selectionMode: selectionWord}},
		{name: "seen cap", m: &model{wordSelectHint: wordSelectHintState{enabled: true, shown: 3}, selectionMode: selectionFlash}},
	} {
		t.Run(test.name, func(t *testing.T) {
			test.m.width, test.m.height = 80, 30
			test.m.Update(mouseSelectionEvent{phase: selectionStart, point: point, lines: lines, at: at, assistant: true})
			test.m.Update(mouseSelectionEvent{phase: selectionStart, point: point, lines: lines, at: at.Add(100 * time.Millisecond), assistant: true})
			if test.m.wordSelectHint.active {
				t.Fatal("hint became active")
			}
		})
	}

	m := &model{
		width: 80, height: 30, wordSelectHint: wordSelectHintState{
			enabled: true, active: true, shown: 1, remaining: 20 * time.Second, nonce: 1, input: "",
		},
	}
	updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: 'a', Text: "a"}))
	m = updated.(*model)
	if command != nil || m.wordSelectHint.active {
		t.Fatalf("edit command=%v active=%v", command != nil, m.wordSelectHint.active)
	}
}

func TestWordSelectHintIgnoresNonAssistantContent(t *testing.T) {
	m := &model{
		width: 80, height: 30, selectionMode: selectionFlash,
		wordSelectHint: wordSelectHintState{enabled: true},
	}
	point := selectionPoint{line: 0, column: 1}
	lines := []string{"user content"}
	at := time.Now()
	m.Update(mouseSelectionEvent{phase: selectionStart, point: point, lines: lines, at: at})
	_, command := m.Update(mouseSelectionEvent{phase: selectionStart, point: point, lines: lines, at: at.Add(100 * time.Millisecond)})
	if command != nil || m.wordSelectHint.active {
		t.Fatalf("command=%v active=%v", command != nil, m.wordSelectHint.active)
	}
}

func TestTranscriptRoleAtVisibleLine(t *testing.T) {
	m := &model{width: 80, height: 12}
	m.transcript.WriteString("You\n\nquestion\n\nGork\n\nanswer")
	m.transcriptMessages = []transcriptMessage{
		{start: 0, offset: 3, role: "user"},
		{start: 15, offset: 19, role: "assistant"},
	}
	total := len(renderMarkdownTheme(m.transcriptText(), m.transcriptRenderWidth(), false, m.colors()))
	if role := m.transcriptRoleAtVisibleLine(0, total); role != "user" {
		t.Fatalf("first role=%q", role)
	}
	if role := m.transcriptRoleAtVisibleLine(total-1, total); role != "assistant" {
		t.Fatalf("last role=%q", role)
	}
}

func TestWordSelectHintAcceptanceRollsBackPersistenceFailure(t *testing.T) {
	m := &model{
		width: 80, height: 30, selectionMode: selectionFlash,
		wordSelectHint: wordSelectHintState{
			enabled: true, active: true, shown: 1, remaining: 20 * time.Second, nonce: 1, input: "",
		},
		persistSelection: func(string) error { return errors.New("read only") },
	}
	updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: 'y', Mod: tea.ModCtrl}))
	m = updated.(*model)
	if command != nil || m.selectionMode != selectionFlash || m.status != "setting update failed" {
		t.Fatalf("command=%v mode=%q status=%q", command != nil, m.selectionMode.canonical(), m.status)
	}
}

func TestWordSelectHintTTLPausesWhileOccluded(t *testing.T) {
	m := &model{
		width: 80, height: 30,
		wordSelectHint: wordSelectHintState{
			enabled: true, active: true, remaining: time.Second, nonce: 4,
		},
		settings: &settingsState{},
	}
	updated, command := m.Update(wordSelectHintTickEvent{nonce: 4})
	m = updated.(*model)
	if command == nil || m.wordSelectHint.remaining != time.Second {
		t.Fatalf("occluded command=%v remaining=%v", command != nil, m.wordSelectHint.remaining)
	}
	m.settings = nil
	updated, command = m.Update(wordSelectHintTickEvent{nonce: 4})
	m = updated.(*model)
	if command == nil || m.wordSelectHint.remaining != 900*time.Millisecond {
		t.Fatalf("visible command=%v remaining=%v", command != nil, m.wordSelectHint.remaining)
	}
}
