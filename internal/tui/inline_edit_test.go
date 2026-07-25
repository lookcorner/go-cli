package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lookcorner/go-cli/internal/session"
)

func prepareInlineEditFixture(t *testing.T) (*model, *modelTUIStreamer) {
	t.Helper()
	m, _ := rewindTUIFixture(t)
	streamer := &modelTUIStreamer{}
	m.runner.Client = streamer
	messages, err := session.Transcript(m.runner.SessionPath)
	if err != nil {
		t.Fatal(err)
	}
	m.replaceTranscript(session.FormatTranscript(messages), messages)
	return m, streamer
}

func TestDoubleClickPreviousUserMessageEditsAndResubmits(t *testing.T) {
	m, streamer := prepareInlineEditFixture(t)
	m.setInput("保留的草稿\nsecond line")
	m.promptImages = nil
	at := time.Unix(100, 0)
	event := mouseSelectionEvent{
		phase: selectionStart, point: selectionPoint{}, lines: []string{"You"}, at: at,
		transcript: true, transcriptMessage: 0,
	}
	updated, _ := m.Update(event)
	m = updated.(*model)
	event.at = at.Add(100 * time.Millisecond)
	updated, command := m.Update(event)
	m = updated.(*model)
	if command != nil || m.inlineEdit == nil || string(m.input) != "first request" || m.scrollFocused {
		t.Fatalf("command=%v edit=%#v input=%q focused=%v", command != nil, m.inlineEdit, m.input, m.scrollFocused)
	}

	m.setInput("修复后的请求\nwith context")
	updated, command = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command == nil || m.rewind == nil || m.rewind.phase != rewindExecuting {
		t.Fatalf("command=%v rewind=%#v", command != nil, m.rewind)
	}
	updated, command = m.Update(command())
	m = updated.(*model)
	if command == nil || !m.running || m.inlineEdit != nil || string(m.input) != "保留的草稿\nsecond line" {
		t.Fatalf("command=%v running=%v edit=%#v input=%q", command != nil, m.running, m.inlineEdit, m.input)
	}
	if !strings.Contains(m.transcript.String(), "修复后的请求\nwith context") || strings.Contains(m.transcript.String(), "second request") {
		t.Fatalf("transcript=%q", m.transcript.String())
	}
	_ = command()
	content, _ := streamer.request.Input[len(streamer.request.Input)-1].Content.(string)
	if content != "修复后的请求\nwith context" {
		t.Fatalf("submitted content=%q", content)
	}
}

func TestInlineEditCancelAndUnchangedRestoreDraft(t *testing.T) {
	for _, key := range []tea.Key{{Code: tea.KeyEsc}, {Code: tea.KeyEnter}} {
		m, _ := prepareInlineEditFixture(t)
		m.setInput("draft")
		if !m.enterInlineEdit(0) {
			t.Fatal("user message was not editable")
		}
		updated, command := m.Update(tea.KeyPressMsg(key))
		m = updated.(*model)
		if command != nil || m.inlineEdit != nil || string(m.input) != "draft" || m.rewind != nil {
			t.Fatalf("key=%v command=%v edit=%#v input=%q rewind=%#v", key.Code, command != nil, m.inlineEdit, m.input, m.rewind)
		}
	}
}

func TestInlineEditEnterAlwaysSubmitsAndModifiedEnterAddsNewline(t *testing.T) {
	m, _ := prepareInlineEditFixture(t)
	m.multiline = true
	if !m.enterInlineEdit(0) {
		t.Fatal("user message was not editable")
	}
	updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter, Mod: tea.ModShift}))
	m = updated.(*model)
	if command != nil || string(m.input) != "first request\n" {
		t.Fatalf("modified enter command=%v input=%q", command != nil, m.input)
	}
	m.insertInput("changed")
	updated, command = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command == nil || m.rewind == nil || m.rewind.phase != rewindExecuting {
		t.Fatalf("plain enter command=%v rewind=%#v", command != nil, m.rewind)
	}

	m, _ = prepareInlineEditFixture(t)
	m.running = true
	if !m.enterInlineEdit(0) || !m.acceptsPaste() {
		t.Fatal("running inline edit did not accept paste")
	}
}

func TestInlineEditOnlyAcceptsUserMessagesAndJumpEnterStartsEdit(t *testing.T) {
	m, _ := prepareInlineEditFixture(t)
	if m.enterInlineEdit(1) {
		t.Fatal("assistant message became editable")
	}
	m.openJump()
	if m.jump == nil {
		t.Fatal("jump picker did not open")
	}
	updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command != nil || m.jump != nil || m.inlineEdit == nil || string(m.input) != "first request" {
		t.Fatalf("command=%v jump=%#v edit=%#v input=%q", command != nil, m.jump, m.inlineEdit, m.input)
	}
}

func TestInlineEditOutsideClickRestoresDraft(t *testing.T) {
	m, _ := prepareInlineEditFixture(t)
	m.setInput("draft")
	if !m.enterInlineEdit(0) {
		t.Fatal("user message was not editable")
	}
	updated, command := m.Update(mouseSelectionEvent{
		phase: selectionStart, point: selectionPoint{}, lines: []string{"other"}, transcript: true, transcriptMessage: 1,
	})
	m = updated.(*model)
	if command != nil || m.inlineEdit != nil || string(m.input) != "draft" || m.selection != nil {
		t.Fatalf("command=%v edit=%#v input=%q selection=%#v", command != nil, m.inlineEdit, m.input, m.selection)
	}
}

func TestInlineEditRewindFailureKeepsEditedText(t *testing.T) {
	m, _ := prepareInlineEditFixture(t)
	if !m.enterInlineEdit(0) {
		t.Fatal("user message was not editable")
	}
	m.setInput("changed")
	updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command == nil {
		t.Fatal("rewind did not start")
	}
	updated, command = m.Update(rewindDoneEvent{err: errors.New("disk full"), serial: m.promptSerial})
	m = updated.(*model)
	if command != nil || m.inlineEdit == nil || m.inlineEdit.submitting || m.rewind != nil || string(m.input) != "changed" || !strings.Contains(m.status, "disk full") {
		t.Fatalf("command=%v edit=%#v rewind=%#v input=%q status=%q", command != nil, m.inlineEdit, m.rewind, m.input, m.status)
	}
}
