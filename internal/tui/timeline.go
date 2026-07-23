package tui

import (
	"sort"
	"strconv"
	"strings"
)

const (
	timelineRailWidth = 2
	timelineMinWidth  = 60
)

type timelineHitKind uint8

const (
	timelineTick timelineHitKind = iota
	timelineUp
	timelineDown
)

type timelineHit struct {
	kind timelineHitKind
	turn int
}

type timelineRail struct {
	x, upRow, ticksRow, downRow int
	start, end, active          int
	upTarget, downTarget        int
}

type timelineHoverEvent struct {
	hit *timelineHit
}

type timelineJumpEvent struct {
	turn int
}

func timelineHitsEqual(left, right *timelineHit) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func (m *model) transcriptRenderWidth() int {
	return max(max(m.width, 20)-m.timelineWidth(), 20)
}

func (m *model) timelineWidth() int {
	if !m.showTimeline || m.width < timelineMinWidth || len(m.jumpEntries()) < 2 || !m.transcriptVisible() {
		return 0
	}
	return timelineRailWidth
}

func (m *model) transcriptVisible() bool {
	return m.mcp == nil && m.sessionSelect == nil && m.forkChoice == nil && m.modelSelect == nil &&
		m.rewind == nil && m.planReview == nil && m.viewer == nil && m.remember == nil
}

func (m *model) computeTimelineRail(lineCount int) *timelineRail {
	entries := m.jumpEntries()
	viewportHeight := m.contentHeight()
	height := viewportHeight - 1
	if m.timelineWidth() == 0 || height < 3 {
		return nil
	}
	maxTicks := height - 2
	topLine := max(lineCount+m.scrollTail-viewportHeight-m.scroll, 0)
	firstBelow := sort.Search(len(entries), func(index int) bool {
		return m.jumpLine(entries[index].message) > topLine
	})
	active, downTarget := max(firstBelow-1, 0), firstBelow
	if downTarget == len(entries) {
		downTarget = -1
	}
	upTarget := sort.Search(len(entries), func(index int) bool {
		return m.jumpLine(entries[index].message) >= topLine
	}) - 1
	start := 0
	if len(entries) > maxTicks {
		tail := len(entries) - maxTicks
		if m.scroll == 0 {
			start = min(active, tail)
		} else {
			start = min(max(active-maxTicks/2, 0), tail)
		}
	}
	end := min(start+maxTicks, len(entries))
	stackTop := (height - (end - start + 2)) / 2
	return &timelineRail{
		x: m.width - timelineRailWidth, upRow: stackTop, ticksRow: stackTop + 1,
		downRow: stackTop + 1 + end - start, start: start, end: end, active: active,
		upTarget: upTarget, downTarget: downTarget,
	}
}

func (r *timelineRail) hit(x, row int) *timelineHit {
	if r == nil || x < r.x || x >= r.x+timelineRailWidth {
		return nil
	}
	switch {
	case row == r.upRow:
		return &timelineHit{kind: timelineUp, turn: r.upTarget}
	case row == r.downRow:
		return &timelineHit{kind: timelineDown, turn: r.downTarget}
	case row >= r.ticksRow && row < r.ticksRow+r.end-r.start:
		return &timelineHit{kind: timelineTick, turn: r.start + row - r.ticksRow}
	default:
		return nil
	}
}

func (m *model) renderTimeline(lines []string, rail *timelineRail) []string {
	if rail == nil {
		return lines
	}
	if m.timelineHover != nil && m.timelineHover.kind == timelineTick {
		lines = m.renderTimelinePreview(lines, rail, m.timelineHover.turn)
	}
	for row := 0; row < len(lines)-1; row++ {
		lines[row] = padDisplayRight(lines[row], rail.x)
	}
	set := func(row int, value string) {
		if row >= 0 && row < len(lines) {
			lines[row] += value
		}
	}
	up, down := ansiDim+" ▴"+ansiReset, ansiDim+" ▾"+ansiReset
	if rail.upTarget >= 0 {
		up = m.colors().heading + " ▴" + ansiReset
	}
	if rail.downTarget >= 0 {
		down = m.colors().heading + " ▾" + ansiReset
	}
	set(rail.upRow, up)
	set(rail.downRow, down)
	for turn := rail.start; turn < rail.end; turn++ {
		row, tick := rail.ticksRow+turn-rail.start, ansiDim+" ─"+ansiReset
		if turn == rail.active {
			tick = m.colors().heading + "━━" + ansiReset
		} else if m.timelineHover != nil && m.timelineHover.kind == timelineTick && m.timelineHover.turn == turn {
			tick = m.colors().heading + "──" + ansiReset
		}
		set(row, tick)
	}
	return lines
}

func (m *model) renderTimelinePreview(lines []string, rail *timelineRail, turn int) []string {
	entries := m.jumpEntries()
	if turn < rail.start || turn >= rail.end || turn >= len(entries) || entries[turn].preview == "" {
		return lines
	}
	maxText := min(max((m.width-timelineRailWidth)/2, 16), 32)
	text := []rune(entries[turn].preview)
	first := fitInputLine(text, maxText)
	text = text[len([]rune(first)):]
	preview := []string{first}
	if len(text) > 0 {
		second := fitInputLine(text, maxText-1)
		if len([]rune(second)) < len(text) {
			second += "…"
		}
		preview = append(preview, second)
	}
	textWidth := 0
	for _, line := range preview {
		textWidth = max(textWidth, displayWidth(line))
	}
	cardWidth := textWidth + 4
	card := []string{"┌" + strings.Repeat("─", cardWidth-2) + "┐"}
	for _, line := range preview {
		card = append(card, "│ "+padDisplayRight(line, textWidth)+" │")
	}
	card = append(card, "└"+strings.Repeat("─", cardWidth-2)+"┘")
	tickRow := rail.ticksRow + turn - rail.start
	top := min(max(tickRow-len(card)/2, 0), max(len(lines)-len(card), 0))
	left := max(rail.x-cardWidth-1, 0)
	for index, row := range card {
		base := fitInputLine([]rune(stripUIANSI(lines[top+index])), left)
		lines[top+index] = padDisplayRight(base, left) + row
	}
	return lines
}

func padDisplayRight(value string, width int) string {
	return value + strings.Repeat(" ", max(width-displayWidth(stripUIANSI(value)), 0))
}

func (m *model) anchorTranscriptMessage(message int) {
	if message < 0 || message >= len(m.transcriptMessages) {
		return
	}
	lines := renderMarkdownTheme(m.transcriptText(), m.transcriptRenderWidth(), false, m.colors())
	target := m.jumpLine(message)
	m.scrollTail = max(target+m.contentHeight()-len(lines), 0)
	m.scroll = len(lines) + m.scrollTail - m.contentHeight() - target
	m.scrollAnchor = new(int)
	*m.scrollAnchor = message
}

func (m *model) clearTranscriptAnchor() {
	m.scroll = max(m.scroll-m.scrollTail, 0)
	m.scrollTail = 0
	m.scrollAnchor = nil
}

func (m *model) jumpTimeline(turn int) {
	entries := m.jumpEntries()
	if turn < 0 || turn >= len(entries) {
		return
	}
	m.selection = nil
	m.selectionClick = selectionClickState{}
	m.timelineHover = nil
	m.anchorTranscriptMessage(entries[turn].message)
	m.status = "jumped to turn " + strconv.Itoa(turn+1)
}
