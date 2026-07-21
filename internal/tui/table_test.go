package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

func TestRenderMarkdownTable(t *testing.T) {
	lines := renderMarkdown("| Animal | Sound |\n| --- | --- |\n| **Zebra** | `Neigh|Noise` |\n| Quick \\| Fox | Ignore |", 40)
	plain := make([]string, len(lines))
	for index, line := range lines {
		plain[index] = stripMarkdownANSI(line)
	}
	rendered := strings.Join(plain, "\n")
	for _, wanted := range []string{"┌", "┬", "┐", "├", "┼", "┤", "└", "┴", "┘", "Zebra", "Neigh|Noise", "Quick | Fox"} {
		if !strings.Contains(rendered, wanted) {
			t.Fatalf("table missing %q:\n%s", wanted, rendered)
		}
	}
	if !strings.Contains(strings.Join(lines, ""), ansiBold) || !strings.Contains(strings.Join(lines, ""), ansiCyan) {
		t.Fatalf("inline styles were lost:\n%s", strings.Join(lines, "\n"))
	}
	for _, line := range lines {
		if markdownVisibleWidth(line) > 40 {
			t.Fatalf("line too wide: %q", line)
		}
	}
}

func TestRenderMarkdownSingleColumnTable(t *testing.T) {
	lines := stripMarkdownLines(renderMarkdown("| Name |\n| --- |\n| Zebra |", 20))
	geometry := tableAt(lines, selectionPoint{line: 1, column: 2})
	if geometry == nil || geometry.columnCount() != 1 || geometry.cellText(lines, tableCell{row: 1}) != "Zebra" {
		t.Fatalf("single-column table=%#v lines=%#v", geometry, lines)
	}
}

func TestRenderMarkdownTableWrapsAtNarrowWidth(t *testing.T) {
	lines := renderMarkdown("A | B\n--- | ---\nabcdefgh | 你好世界", 15)
	if len(lines) < 6 {
		t.Fatalf("table did not wrap: %#v", lines)
	}
	for _, line := range lines {
		if markdownVisibleWidth(line) > 15 {
			t.Fatalf("line too wide: %q", line)
		}
	}
	geometry := tableAt(stripMarkdownLines(lines), selectionPoint{line: 1, column: 2})
	if geometry == nil || geometry.cellText(stripMarkdownLines(lines), tableCell{row: 1, column: 1}) != "你好 世界" {
		t.Fatalf("wrapped cell was not reconstructed: %#v", geometry)
	}
}

func TestTablePartialSelectionAndDeadZoneLatch(t *testing.T) {
	lines := stripMarkdownLines(renderMarkdown("Name | Kind\n--- | ---\nabcdefgh | value", 15))
	geometry := tableAt(lines, selectionPoint{line: 3, column: 2})
	cell := tableCell{row: 1, column: 0}
	fragments := geometry.rows[cell.row]
	selection := &textSelection{
		anchor: selectionPoint{line: fragments[0].line, column: fragments[0].left + 2},
		head:   selectionPoint{line: fragments[2].line, column: fragments[2].left + 2},
		lines:  lines, moved: true, table: geometry, fromCell: cell, toCell: cell,
	}
	if got := selection.text(); got != "bcd ef" {
		t.Fatalf("partial wrapped selection=%q", got)
	}
	if _, ok := geometry.cellAt(selectionPoint{line: fragments[0].line, column: fragments[0].left}, true); ok {
		t.Fatal("outer padding was treated as content")
	}
	m := &model{selectionMode: selectionHold, selection: selection}
	updated, _ := m.Update(mouseSelectionEvent{phase: selectionMove, point: selectionPoint{line: geometry.top + 1, column: geometry.columns[1]}})
	m = updated.(*model)
	if m.selection.toCell != cell {
		t.Fatalf("divider changed latched cell: %#v", m.selection.toCell)
	}
	if got := geometry.latchedCell(selectionPoint{line: geometry.bottom + 5, column: geometry.columns[len(geometry.columns)-1] + 5}, cell); got != (tableCell{row: len(geometry.rows) - 1, column: geometry.columnCount() - 1}) {
		t.Fatalf("outside drag did not clamp to last cell: %#v", got)
	}
}

func TestTableGeometryAndTSVSelection(t *testing.T) {
	lines := stripMarkdownLines(renderMarkdown("Animal | Sound\n--- | ---\nZebraOne | NeighNoise\nBravoCat | MeowNoise", 40))
	geometry := tableAt(lines, selectionPoint{line: 3, column: 3})
	if geometry == nil {
		t.Fatal("table geometry not detected")
	}
	if got := geometry.tsv(lines, tableCell{row: 2, column: 1}, tableCell{row: 1, column: 0}); got != "ZebraOne\tNeighNoise\nBravoCat\tMeowNoise" {
		t.Fatalf("reversed TSV=%q", got)
	}
	if tableAt([]string{"┌──┬──┐", "│ a│ b│"}, selectionPoint{}) != nil {
		t.Fatal("unclosed grid accepted")
	}
	if tableAt([]string{"┌──┐ junk", "│ a│", "└──┘"}, selectionPoint{}) != nil {
		t.Fatal("grid with trailing content accepted")
	}
	wide := stripMarkdownLines(renderMarkdown("名称 | 值\n--- | ---\n你好 | ok", 20))
	if tableAt(wide, selectionPoint{line: 3, column: 3}) == nil {
		t.Fatal("wide-glyph grid not detected")
	}
}

func TestTableMouseSelection(t *testing.T) {
	lines := stripMarkdownLines(renderMarkdown("Animal | Sound\n--- | ---\nZebraOne | NeighNoise\nBravoCat | MeowNoise", 40))
	geometry := tableAt(lines, selectionPoint{line: 3, column: 3})
	left := geometry.columns[0] + 2
	right := geometry.columns[1] + 2
	rowOne := geometry.rows[1][0].line
	rowTwo := geometry.rows[2][0].line

	m := &model{selectionMode: selectionHold}
	updated, _ := m.Update(mouseSelectionEvent{phase: selectionStart, point: selectionPoint{line: rowOne, column: left}, lines: lines})
	m = updated.(*model)
	updated, _ = m.Update(mouseSelectionEvent{phase: selectionMove, point: selectionPoint{line: rowTwo, column: right}})
	m = updated.(*model)
	updated, command := m.Update(mouseSelectionEvent{phase: selectionRelease, point: selectionPoint{line: rowTwo, column: right}})
	m = updated.(*model)
	if command == nil || fmt.Sprint(command()) != "ZebraOne\tNeighNoise\nBravoCat\tMeowNoise" {
		t.Fatalf("table drag copied %q", fmt.Sprint(command()))
	}
	highlighted := m.selection.highlightedLines(append([]string(nil), lines...))
	if strings.Contains(highlighted[rowOne][:1], "\x1b[7m") || !strings.Contains(highlighted[rowOne], "\x1b[7m") {
		t.Fatalf("invalid table highlight: %q", highlighted[rowOne])
	}
}

func TestTableTripleClickCellAndBorder(t *testing.T) {
	lines := stripMarkdownLines(renderMarkdown("Animal | Sound\n--- | ---\nZebraOne | NeighNoise", 40))
	geometry := tableAt(lines, selectionPoint{})
	t0 := time.Unix(300, 0)
	triple := func(point selectionPoint) string {
		m := &model{selectionMode: selectionWord, wordSeparators: defaultWordSeparators}
		var command tea.Cmd
		for click := 0; click < 3; click++ {
			updated, next := m.Update(mouseSelectionEvent{phase: selectionStart, point: point, lines: lines, at: t0.Add(time.Duration(click) * 100 * time.Millisecond)})
			m, command = updated.(*model), next
			if click == 0 {
				updated, _ = m.Update(mouseSelectionEvent{phase: selectionRelease, point: point})
				m = updated.(*model)
			}
		}
		if command == nil {
			return ""
		}
		return fmt.Sprint(command())
	}
	cellPoint := selectionPoint{line: geometry.rows[1][0].line, column: geometry.columns[0] + 2}
	if got := triple(cellPoint); got != "ZebraOne" {
		t.Fatalf("cell triple click=%q", got)
	}
	if got := triple(selectionPoint{line: geometry.top, column: geometry.columns[0]}); got != "Animal\tSound\nZebraOne\tNeighNoise" {
		t.Fatalf("border triple click=%q", got)
	}
}

func TestTableBorderDragRemainsLinear(t *testing.T) {
	lines := stripMarkdownLines(renderMarkdown("A | B\n--- | ---\none | two", 20))
	m := &textSelection{anchor: selectionPoint{}, head: selectionPoint{column: 2}, lines: lines, moved: true}
	if got := m.text(); got != "┌──" {
		t.Fatalf("border selection=%q", got)
	}
}

func stripMarkdownLines(lines []string) []string {
	plain := make([]string, len(lines))
	for index, line := range lines {
		plain[index] = stripMarkdownANSI(line)
	}
	return plain
}
