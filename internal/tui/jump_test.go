package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

func jumpTestModel(prompts ...string) *model {
	m := &model{width: 30, height: 14, status: "ready"}
	for index, prompt := range prompts {
		m.beginTurn(prompt)
		m.transcript.WriteString("\n" + strings.Repeat("answer text wraps here ", index+3))
	}
	m.running = false
	m.status = "ready"
	return m
}

func TestJumpRequiresTwoTurnsAndAppearsInHelp(t *testing.T) {
	m := jumpTestModel("only turn")
	m.setInput("/jump")
	updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command != nil || m.jump != nil || m.status != "Nothing to jump to yet" {
		t.Fatalf("command=%v jump=%#v status=%q", command != nil, m.jump, m.status)
	}

	m = &model{width: 80, height: 24, status: "ready"}
	m.setInput("/help")
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if !strings.Contains(updated.(*model).transcript.String(), "`/jump`") {
		t.Fatalf("help omitted /jump: %q", updated.(*model).transcript.String())
	}
}

func TestJumpEntriesAreOldestFirstWithSingleLinePreviews(t *testing.T) {
	m := jumpTestModel("first\nmultiline prompt", "second prompt", "third prompt")
	entries := m.jumpEntries()
	if len(entries) != 3 {
		t.Fatalf("entries=%#v", entries)
	}
	want := []string{"first multiline prompt", "second prompt", "third prompt"}
	for index := range want {
		if entries[index].preview != want[index] {
			t.Fatalf("entry %d preview=%q want=%q", index, entries[index].preview, want[index])
		}
	}

	m.scroll = m.maxTranscriptScroll()
	m.openJump()
	if m.jump == nil || m.jump.selected != 0 {
		t.Fatalf("jump=%#v", m.jump)
	}
	view := stripUIANSI(m.View().Content)
	if !strings.Contains(view, "Jump to which turn?") || !strings.Contains(view, "1  first multiline prom") {
		t.Fatalf("picker not rendered:\n%s", view)
	}
}

func TestJumpPreviewClampsAndEscapeRestoresViewport(t *testing.T) {
	m := jumpTestModel("first prompt", "second prompt", "third prompt")
	m.scroll = m.maxTranscriptScroll()
	restored := m.scroll
	m.openJump()

	updated, _ := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown}))
	m = updated.(*model)
	if m.jump.selected != 1 {
		t.Fatalf("selected=%d", m.jump.selected)
	}
	lines := renderMarkdownTheme(m.transcriptText(), m.width, false, m.colors())
	maxStart := max(len(lines)-m.contentHeight(), 0)
	wantStart := min(m.jumpLine(m.jump.entries[1].message), maxStart)
	if got := maxStart - m.scroll; got != wantStart {
		t.Fatalf("preview start=%d want=%d scroll=%d", got, wantStart, m.scroll)
	}

	for range 10 {
		updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: 'j', Text: "j"}))
		m = updated.(*model)
	}
	if m.jump.selected != 2 {
		t.Fatalf("down clamp=%d", m.jump.selected)
	}
	for range 10 {
		updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: 'k', Text: "k"}))
		m = updated.(*model)
	}
	if m.jump.selected != 0 {
		t.Fatalf("up clamp=%d", m.jump.selected)
	}

	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEsc}))
	m = updated.(*model)
	if m.jump != nil || m.scroll != restored || m.status != "ready" {
		t.Fatalf("jump=%#v scroll=%d want=%d status=%q", m.jump, m.scroll, restored, m.status)
	}
}

func TestJumpEnterKeepsPreviewAndHandlesTimestampsAndCompactMode(t *testing.T) {
	m := jumpTestModel("first prompt", "second prompt", "third prompt")
	m.height = 18
	m.showTimestamps = true
	for index := range m.transcriptMessages {
		m.transcriptMessages[index].at = time.Date(2026, 7, 24, 9+index, 5, 0, 0, time.Local)
	}
	m.scroll = m.maxTranscriptScroll()
	entries := m.jumpEntries()
	lines := renderMarkdownTheme(m.transcriptText(), m.width, false, m.colors())
	var userLines []int
	for index, line := range lines {
		if strings.HasPrefix(stripUIANSI(line), "You") {
			userLines = append(userLines, index)
		}
	}
	if len(userLines) != 3 || m.jumpLine(entries[1].message) != userLines[1] {
		t.Fatalf("user lines=%v second jump line=%d", userLines, m.jumpLine(entries[1].message))
	}
	m.openJump()
	updated, _ := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown}))
	m = updated.(*model)
	previewScroll := m.scroll
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if m.jump != nil || m.scroll != previewScroll || m.status != "ready" {
		t.Fatalf("jump=%#v scroll=%d want=%d status=%q", m.jump, m.scroll, previewScroll, m.status)
	}
}

func TestJumpBlocksSideQuestionAndFitsWideText(t *testing.T) {
	m := jumpTestModel("第一条很长的中文问题", "第二条问题")
	m.btwRunning = true
	m.openJump()
	if m.jump != nil || m.status != "wait for the side question before jumping" {
		t.Fatalf("jump=%#v status=%q", m.jump, m.status)
	}
	if row := fitJumpRow("第一条很长的中文问题", 10); displayWidth(row) != 10 {
		t.Fatalf("row=%q width=%d", row, displayWidth(row))
	}
}
