package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestTimelineCommandPersistsTogglesAndRollsBack(t *testing.T) {
	var persisted []bool
	m := jumpTestModel("first", "second")
	m.width = 80
	m.persistTimeline = func(enabled bool) error {
		persisted = append(persisted, enabled)
		return nil
	}
	before := m.transcript.String()
	m.setInput("/timeline")
	updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command != nil || !m.showTimeline || len(persisted) != 1 || !persisted[0] || m.status != "Timeline sidebar: on" || m.transcript.String() != before || !strings.Contains(stripUIANSI(m.View().Content), "━━") {
		t.Fatalf("command=%v enabled=%v persisted=%v status=%q view=%q", command != nil, m.showTimeline, persisted, m.status, stripUIANSI(m.View().Content))
	}
	m.setInput("/timeline ignored")
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if m.showTimeline || len(persisted) != 2 || persisted[1] || m.status != "Timeline sidebar: off" {
		t.Fatalf("enabled=%v persisted=%v status=%q", m.showTimeline, persisted, m.status)
	}
	m.setInput("/help")
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if !strings.Contains(updated.(*model).transcript.String(), "`/timeline`") {
		t.Fatalf("help omitted timeline: %q", updated.(*model).transcript.String())
	}

	failed := jumpTestModel("first", "second")
	failed.persistTimeline = func(bool) error { return errors.New("disk full") }
	failed.setInput("/timeline")
	updated, command = failed.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	failed = updated.(*model)
	if command != nil || failed.showTimeline || failed.status != "persist timeline: disk full" || len(failed.jumpEntries()) != 2 {
		t.Fatalf("command=%v enabled=%v status=%q", command != nil, failed.showTimeline, failed.status)
	}
}

func TestTimelineRailEligibilityAndWindowsAroundViewport(t *testing.T) {
	m := jumpTestModel("first")
	m.showTimeline = true
	m.width = 80
	if width := m.timelineWidth(); width != 0 {
		t.Fatalf("one-turn width=%d", width)
	}
	m.beginTurn("second")
	m.running = false
	m.width = timelineMinWidth - 1
	if width := m.timelineWidth(); width != 0 {
		t.Fatalf("narrow width=%d", width)
	}
	m.width = timelineMinWidth
	if width := m.timelineWidth(); width != timelineRailWidth {
		t.Fatalf("eligible width=%d", width)
	}

	m = &model{width: 80, height: 12, showTimeline: true}
	for turn := 0; turn < 30; turn++ {
		m.beginTurn(fmt.Sprintf("turn %d", turn+1))
		m.transcript.WriteString("\nanswer")
	}
	m.running = false
	lines := renderMarkdownTheme(m.transcriptText(), m.transcriptRenderWidth(), false, m.colors())
	rail := m.computeTimelineRail(len(lines))
	if rail == nil || rail.end-rail.start != m.contentHeight()-3 || rail.active < rail.start || rail.active >= rail.end {
		t.Fatalf("rail=%#v height=%d", rail, m.contentHeight())
	}
}

func TestTimelineMouseHoverAndClickTopAnchorTurns(t *testing.T) {
	m := jumpTestModel("first prompt", "second prompt", "third prompt")
	m.width, m.height, m.showTimeline = 80, 14, true
	m.scroll = m.maxTranscriptScroll()
	view := m.View()
	lines := renderMarkdownTheme(m.transcriptText(), m.transcriptRenderWidth(), false, m.colors())
	rail := m.computeTimelineRail(len(lines))
	if rail == nil {
		t.Fatal("timeline rail missing")
	}
	row := rail.ticksRow + 1 - rail.start
	hover := view.OnMouse(tea.MouseMotionMsg(tea.Mouse{X: rail.x, Y: row + 1, Button: tea.MouseNone}))
	if hover == nil {
		t.Fatal("tick hover produced no event")
	}
	updated, _ := m.Update(hover())
	m = updated.(*model)
	if m.timelineHover == nil || m.timelineHover.turn != 1 || !strings.Contains(stripUIANSI(m.View().Content), "second prompt") {
		t.Fatalf("hover=%#v view=%q", m.timelineHover, stripUIANSI(m.View().Content))
	}
	for index, line := range strings.Split(m.View().Content, "\n") {
		if width := displayWidth(stripUIANSI(line)); width > m.width {
			t.Fatalf("view line %d width=%d: %q", index, width, stripUIANSI(line))
		}
	}

	view = m.View()
	click := view.OnMouse(tea.MouseClickMsg(tea.Mouse{X: rail.x, Y: row + 1, Button: tea.MouseLeft}))
	if click == nil {
		t.Fatal("tick click produced no event")
	}
	updated, _ = m.Update(click())
	m = updated.(*model)
	entries := m.jumpEntries()
	if m.scrollAnchor == nil || *m.scrollAnchor != entries[1].message || m.timelineHover != nil || m.status != "jumped to turn 2" {
		t.Fatalf("anchor=%v hover=%#v status=%q", m.scrollAnchor, m.timelineHover, m.status)
	}
	lines = renderMarkdownTheme(m.transcriptText(), m.transcriptRenderWidth(), false, m.colors())
	if top := len(lines) + m.scrollTail - m.contentHeight() - m.scroll; top != m.jumpLine(entries[1].message) {
		t.Fatalf("top=%d target=%d tail=%d", top, m.jumpLine(entries[1].message), m.scrollTail)
	}

	m.jumpTimeline(2)
	if m.scrollTail == 0 {
		t.Fatal("last short turn was not over-scrolled to the top")
	}
	updated, _ = m.Update(tea.WindowSizeMsg{Width: 62, Height: 18})
	m = updated.(*model)
	lines = renderMarkdownTheme(m.transcriptText(), m.transcriptRenderWidth(), false, m.colors())
	if top := len(lines) + m.scrollTail - m.contentHeight() - m.scroll; top != m.jumpLine(entries[2].message) {
		t.Fatalf("resized top=%d target=%d", top, m.jumpLine(entries[2].message))
	}
}

func TestTimelineChevronsStepAndDisabledEndsDoNothing(t *testing.T) {
	m := jumpTestModel("first", "second", "third")
	m.width, m.height, m.showTimeline = 80, 12, true
	m.jumpTimeline(0)
	lines := renderMarkdownTheme(m.transcriptText(), m.transcriptRenderWidth(), false, m.colors())
	rail := m.computeTimelineRail(len(lines))
	if rail == nil || rail.upTarget != -1 || rail.downTarget != 1 {
		t.Fatalf("top rail=%#v", rail)
	}
	view := m.View()
	if command := view.OnMouse(tea.MouseClickMsg(tea.Mouse{X: rail.x, Y: rail.upRow + 1, Button: tea.MouseLeft})); command != nil {
		t.Fatal("disabled up chevron produced an event")
	}
	down := view.OnMouse(tea.MouseClickMsg(tea.Mouse{X: rail.x, Y: rail.downRow + 1, Button: tea.MouseLeft}))
	if down == nil {
		t.Fatal("enabled down chevron produced no event")
	}
	updated, _ := m.Update(down())
	m = updated.(*model)
	entries := m.jumpEntries()
	if m.scrollAnchor == nil || *m.scrollAnchor != entries[1].message {
		t.Fatalf("down anchor=%v want=%d", m.scrollAnchor, entries[1].message)
	}
	lines = renderMarkdownTheme(m.transcriptText(), m.transcriptRenderWidth(), false, m.colors())
	rail = m.computeTimelineRail(len(lines))
	if rail.upTarget != 0 || rail.downTarget != 2 {
		t.Fatalf("middle rail=%#v", rail)
	}
}

func TestTurnPreviewUsesFirstNonemptyLineAndCapsLength(t *testing.T) {
	if got := turnPreview("\n  first line  \nsecond line"); got != "first line" {
		t.Fatalf("preview=%q", got)
	}
	got := turnPreview(strings.Repeat("x", 500))
	if len([]rune(got)) != 120 || !strings.HasSuffix(got, "…") {
		t.Fatalf("long preview length=%d suffix=%q", len([]rune(got)), got[len(got)-3:])
	}
}
