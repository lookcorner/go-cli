package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestInputClearDetectorFiresAtResidueThreshold(t *testing.T) {
	var detector inputClearDetector
	for length := 0; length < 30; length++ {
		if detector.observeUserEdit(length, length+1) {
			t.Fatalf("fired while typing at %d", length)
		}
	}
	for length := 30; length > 6; length-- {
		if detector.observeUserEdit(length, length-1) {
			t.Fatalf("fired above residue at %d", length)
		}
	}
	if !detector.observeUserEdit(6, 5) {
		t.Fatal("did not fire at residue threshold")
	}
	if detector.observeUserEdit(5, 0) {
		t.Fatal("fired again without a new peak")
	}
}

func TestInputClearDetectorResyncsProgrammaticChanges(t *testing.T) {
	var detector inputClearDetector
	for length := 0; length < 30; length++ {
		detector.observeUserEdit(length, length+1)
	}
	if detector.observeUserEdit(0, 1) || detector.observeUserEdit(1, 0) {
		t.Fatal("programmatic clear reused a stale peak")
	}
	if !detector.observeUserEdit(80, 0) {
		t.Fatal("user wipe of a restored long draft did not fire")
	}
}

func TestContextualUndoHintAndUndo(t *testing.T) {
	m := &model{undoHint: contextualHintState{enabled: true}, status: "ready"}
	m.setInput(strings.Repeat("x", 25))
	command := m.editInput(tea.KeyPressMsg(tea.Key{Code: 'u', Mod: tea.ModCtrl}))
	if m.status != "Input cleared · ctrl+z to undo" || m.undoHint.shown != 1 || len(m.input) != 0 {
		t.Fatalf("status=%q shown=%d input=%q", m.status, m.undoHint.shown, m.input)
	}
	if command == nil {
		t.Fatal("contextual hint did not schedule expiry")
	}
	m.editInput(tea.KeyPressMsg(tea.Key{Code: 'z', Mod: tea.ModCtrl}))
	if string(m.input) != strings.Repeat("x", 25) || m.status != "ready" || m.undoHint.shown != 1 {
		t.Fatalf("status=%q shown=%d input=%q", m.status, m.undoHint.shown, m.input)
	}
}

func TestContextualUndoHintExpiryDoesNotOverwriteNewStatus(t *testing.T) {
	m := &model{undoHint: contextualHintState{enabled: true}, status: "ready"}
	m.setInput(strings.Repeat("x", 25))
	if command := m.editInput(tea.KeyPressMsg(tea.Key{Code: 'u', Mod: tea.ModCtrl})); command == nil {
		t.Fatal("contextual hint did not schedule expiry")
	}
	event := contextualUndoClearEvent{nonce: m.undoHint.nonce}
	m.status = "settings"
	updated, next := m.Update(event)
	m = updated.(*model)
	if next != nil || m.status != "settings" {
		t.Fatalf("command=%v status=%q", next != nil, m.status)
	}
}

func TestContextualUndoHintGateAndSessionCap(t *testing.T) {
	disabled := &model{status: "ready"}
	disabled.setInput(strings.Repeat("x", 25))
	disabled.editInput(tea.KeyPressMsg(tea.Key{Code: 'u', Mod: tea.ModCtrl}))
	disabled.editInput(tea.KeyPressMsg(tea.Key{Code: 'z', Mod: tea.ModCtrl}))
	if disabled.undoHint.shown != 0 || string(disabled.input) != strings.Repeat("x", 25) {
		t.Fatalf("shown=%d input=%q", disabled.undoHint.shown, disabled.input)
	}

	m := &model{undoHint: contextualHintState{enabled: true}}
	for iteration := 0; iteration < 4; iteration++ {
		m.setInput(strings.Repeat("x", 25))
		m.editInput(tea.KeyPressMsg(tea.Key{Code: 'u', Mod: tea.ModCtrl}))
	}
	if m.undoHint.shown != 3 {
		t.Fatalf("shown=%d", m.undoHint.shown)
	}
}

func TestShortDraftDoesNotShowContextualUndoHint(t *testing.T) {
	m := &model{undoHint: contextualHintState{enabled: true}, status: "ready"}
	m.setInput(strings.Repeat("x", 19))
	m.editInput(tea.KeyPressMsg(tea.Key{Code: 'u', Mod: tea.ModCtrl}))
	if m.status != "ready" || m.undoHint.shown != 0 {
		t.Fatalf("status=%q shown=%d", m.status, m.undoHint.shown)
	}
}
