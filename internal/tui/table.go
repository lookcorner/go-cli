package tui

import "strings"

type tableCell struct{ row, column int }

type tableFragment struct {
	line        int
	left, right int
}

type tableGeometry struct {
	top, bottom int
	columns     []int
	rows        [][]tableFragment
}

func tableAt(lines []string, point selectionPoint) *tableGeometry {
	for top := min(point.line, len(lines)-1); top >= 0; top-- {
		columns, ok := tableBorder(lines[top], '┌', '┬', '┐')
		if !ok {
			continue
		}
		if geometry := parseTableGeometry(lines, top, columns); geometry != nil && point.line <= geometry.bottom {
			return geometry
		}
	}
	return nil
}

func parseTableGeometry(lines []string, top int, columns []int) *tableGeometry {
	geometry := &tableGeometry{top: top, columns: columns}
	fragments := make([]tableFragment, 0)
	for line := top + 1; line < len(lines); line++ {
		if positions, ok := tableBorder(lines[line], '└', '┴', '┘'); ok && equalInts(positions, columns) {
			if len(fragments) == 0 {
				return nil
			}
			geometry.rows = append(geometry.rows, splitTableFragments(fragments, columns))
			geometry.bottom = line
			return geometry
		}
		if positions, ok := tableBorder(lines[line], '├', '┼', '┤'); ok && equalInts(positions, columns) {
			if len(fragments) == 0 {
				return nil
			}
			geometry.rows = append(geometry.rows, splitTableFragments(fragments, columns))
			fragments = nil
			continue
		}
		if !tableContentLine(lines[line], columns) {
			return nil
		}
		fragments = append(fragments, tableFragment{line: line})
	}
	return nil
}

func tableBorder(line string, left, middle, right rune) ([]int, bool) {
	width := displayWidth(line)
	start := -1
	for column := 0; column < width; column++ {
		if selectDisplayColumns(line, column, column) == string(left) {
			start = column
			break
		}
	}
	if start < 0 {
		return nil, false
	}
	positions := []int{start}
	for column := start + 1; column < width; column++ {
		glyph := selectDisplayColumns(line, column, column)
		switch glyph {
		case "─":
		case string(middle):
			positions = append(positions, column)
		case string(right):
			positions = append(positions, column)
			trailing := selectDisplayColumns(line, column+1, width-1)
			return positions, len(positions) >= 2 && strings.TrimSpace(trailing) == ""
		default:
			return nil, false
		}
	}
	return nil, false
}

func tableContentLine(line string, columns []int) bool {
	for _, column := range columns {
		if selectDisplayColumns(line, column, column) != "│" {
			return false
		}
	}
	return strings.TrimSpace(selectDisplayColumns(line, columns[len(columns)-1]+1, displayWidth(line)-1)) == ""
}

func splitTableFragments(lines []tableFragment, columns []int) []tableFragment {
	result := make([]tableFragment, 0, len(lines)*(len(columns)-1))
	for _, fragment := range lines {
		for column := 0; column < len(columns)-1; column++ {
			result = append(result, tableFragment{line: fragment.line, left: columns[column] + 1, right: columns[column+1] - 1})
		}
	}
	return result
}

func (g *tableGeometry) columnCount() int { return len(g.columns) - 1 }

func (g *tableGeometry) cellAt(point selectionPoint, contentOnly bool) (tableCell, bool) {
	for row, fragments := range g.rows {
		for _, fragment := range fragments {
			if fragment.line != point.line {
				continue
			}
			for cellColumn := 0; cellColumn < g.columnCount(); cellColumn++ {
				if fragment.left != g.columns[cellColumn]+1 {
					continue
				}
				left, right := fragment.left, fragment.right
				if contentOnly {
					left++
					right--
				}
				if point.column >= left && point.column <= right {
					return tableCell{row: row, column: cellColumn}, true
				}
			}
		}
	}
	return tableCell{}, false
}

func (g *tableGeometry) latchedCell(point selectionPoint, current tableCell) tableCell {
	if cell, ok := g.cellAt(point, true); ok {
		return cell
	}
	if point.line <= g.top {
		current.row = 0
	} else if point.line >= g.bottom {
		current.row = len(g.rows) - 1
	}
	if point.column <= g.columns[0] {
		current.column = 0
	} else if point.column >= g.columns[len(g.columns)-1] {
		current.column = g.columnCount() - 1
	} else if point.line <= g.top || point.line >= g.bottom {
		for column := 0; column < g.columnCount(); column++ {
			if point.column > g.columns[column] && point.column < g.columns[column+1] {
				current.column = column
				break
			}
		}
	}
	return current
}

func (g *tableGeometry) partialText(lines []string, cell tableCell, first, last selectionPoint) string {
	if first.line > last.line || first.line == last.line && first.column > last.column {
		first, last = last, first
	}
	parts := make([]string, 0)
	for _, fragment := range g.rows[cell.row] {
		if fragment.left != g.columns[cell.column]+1 || fragment.line < first.line || fragment.line > last.line {
			continue
		}
		from, to := fragment.left+1, fragment.right-1
		if fragment.line == first.line {
			from = min(max(first.column, from), to)
		}
		if fragment.line == last.line {
			to = min(max(last.column, from), to)
		}
		if part := strings.TrimSpace(selectDisplayColumns(lines[fragment.line], from, to)); part != "" {
			parts = append(parts, part)
		}
	}
	return strings.Join(parts, " ")
}

func (g *tableGeometry) partialRanges(cell tableCell, first, last selectionPoint) map[int][][2]int {
	if first.line > last.line || first.line == last.line && first.column > last.column {
		first, last = last, first
	}
	ranges := make(map[int][][2]int)
	for _, fragment := range g.rows[cell.row] {
		if fragment.left != g.columns[cell.column]+1 || fragment.line < first.line || fragment.line > last.line {
			continue
		}
		from, to := fragment.left+1, fragment.right-1
		if fragment.line == first.line {
			from = min(max(first.column, from), to)
		}
		if fragment.line == last.line {
			to = min(max(last.column, from), to)
		}
		ranges[fragment.line] = append(ranges[fragment.line], [2]int{from, to})
	}
	return ranges
}

func (g *tableGeometry) cellText(lines []string, cell tableCell) string {
	parts := make([]string, 0)
	for _, fragment := range g.rows[cell.row] {
		if fragment.left != g.columns[cell.column]+1 {
			continue
		}
		part := strings.TrimSpace(selectDisplayColumns(lines[fragment.line], fragment.left, fragment.right))
		part = strings.ReplaceAll(part, "\t", " ")
		if part != "" {
			parts = append(parts, part)
		}
	}
	return strings.Join(parts, " ")
}

func (g *tableGeometry) tsv(lines []string, first, last tableCell) string {
	if first.row > last.row {
		first.row, last.row = last.row, first.row
	}
	if first.column > last.column {
		first.column, last.column = last.column, first.column
	}
	rows := make([]string, 0, last.row-first.row+1)
	for row := first.row; row <= last.row; row++ {
		cells := make([]string, 0, last.column-first.column+1)
		for column := first.column; column <= last.column; column++ {
			cells = append(cells, g.cellText(lines, tableCell{row: row, column: column}))
		}
		rows = append(rows, strings.Join(cells, "\t"))
	}
	return strings.Join(rows, "\n")
}

func (g *tableGeometry) ranges(first, last tableCell) map[int][][2]int {
	if first.row > last.row {
		first.row, last.row = last.row, first.row
	}
	if first.column > last.column {
		first.column, last.column = last.column, first.column
	}
	ranges := make(map[int][][2]int)
	for row := first.row; row <= last.row; row++ {
		for _, fragment := range g.rows[row] {
			for column := first.column; column <= last.column; column++ {
				if fragment.left == g.columns[column]+1 {
					ranges[fragment.line] = append(ranges[fragment.line], [2]int{fragment.left + 1, fragment.right - 1})
				}
			}
		}
	}
	return ranges
}

func equalInts(left, right []int) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
