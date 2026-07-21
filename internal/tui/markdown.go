package tui

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	ansiReset     = "\x1b[0m"
	ansiBold      = "\x1b[1m"
	ansiDim       = "\x1b[2m"
	ansiItalic    = "\x1b[3m"
	ansiUnderline = "\x1b[4m"
	ansiCyan      = "\x1b[36m"
	ansiYellow    = "\x1b[33m"
)

type markdownSpan struct {
	text  string
	style string
}

func renderMarkdown(value string, width int) []string {
	if width < 1 {
		width = 1
	}
	var lines []string
	inCode := false
	rawLines := strings.Split(value, "\n")
	for index := 0; index < len(rawLines); index++ {
		raw := rawLines[index]
		trimmed := strings.TrimSpace(raw)
		if strings.HasPrefix(trimmed, "```") {
			inCode = !inCode
			if inCode && strings.TrimSpace(strings.TrimPrefix(trimmed, "```")) != "" {
				language := strings.TrimSpace(strings.TrimPrefix(trimmed, "```"))
				lines = append(lines, wrapMarkdownSpans([]markdownSpan{{text: language, style: ansiDim}}, width)...)
			}
			continue
		}
		if inCode {
			lines = append(lines, wrapMarkdownSpans([]markdownSpan{{text: "  " + raw, style: ansiCyan}}, width)...)
			continue
		}
		if index+1 < len(rawLines) {
			if table, consumed := renderMarkdownTable(rawLines[index:], width); consumed > 0 {
				lines = append(lines, table...)
				index += consumed - 1
				continue
			}
		}
		spans := markdownLine(raw)
		if len(spans) == 0 {
			lines = append(lines, "")
			continue
		}
		lines = append(lines, wrapMarkdownSpans(spans, width)...)
	}
	return lines
}

func renderMarkdownTable(lines []string, width int) ([]string, int) {
	header, ok := splitMarkdownTableRow(lines[0])
	if !ok || len(header) < 1 {
		return nil, 0
	}
	delimiter, ok := splitMarkdownTableRow(lines[1])
	if !ok || len(delimiter) != len(header) {
		return nil, 0
	}
	for _, cell := range delimiter {
		marker := strings.TrimSpace(cell)
		marker = strings.TrimPrefix(strings.TrimSuffix(marker, ":"), ":")
		if len(marker) < 3 || strings.Trim(marker, "-") != "" {
			return nil, 0
		}
	}
	rows := [][]string{header}
	consumed := 2
	for consumed < len(lines) {
		row, valid := splitMarkdownTableRow(lines[consumed])
		if !valid || len(row) != len(header) {
			break
		}
		rows = append(rows, row)
		consumed++
	}
	columnWidths := markdownTableWidths(rows, width)
	if columnWidths == nil {
		return nil, 0
	}
	border := func(left, middle, right string) string {
		parts := make([]string, len(columnWidths))
		for index, cellWidth := range columnWidths {
			parts[index] = strings.Repeat("─", cellWidth+2)
		}
		return left + strings.Join(parts, middle) + right
	}
	result := []string{border("┌", "┬", "┐")}
	for rowIndex, row := range rows {
		wrapped := make([][]string, len(row))
		height := 1
		for column, cell := range row {
			wrapped[column] = wrapMarkdownSpans(inlineMarkdown(strings.TrimSpace(cell)), columnWidths[column])
			height = max(height, len(wrapped[column]))
		}
		for line := 0; line < height; line++ {
			var rendered strings.Builder
			rendered.WriteString("│")
			for column := range row {
				part := ""
				if line < len(wrapped[column]) {
					part = wrapped[column][line]
				}
				rendered.WriteString(" ")
				rendered.WriteString(part)
				rendered.WriteString(strings.Repeat(" ", max(columnWidths[column]-markdownANSIWidth(part), 0)+1))
				rendered.WriteString("│")
			}
			result = append(result, rendered.String())
		}
		if rowIndex < len(rows)-1 {
			result = append(result, border("├", "┼", "┤"))
		}
	}
	result = append(result, border("└", "┴", "┘"))
	return result, consumed
}

func splitMarkdownTableRow(line string) ([]string, bool) {
	line = strings.TrimSpace(line)
	if !strings.Contains(line, "|") {
		return nil, false
	}
	if strings.HasPrefix(line, "|") {
		line = line[1:]
	}
	if strings.HasSuffix(line, "|") && !strings.HasSuffix(line, `\|`) {
		line = line[:len(line)-1]
	}
	var cells []string
	var cell strings.Builder
	inCode, escaped := false, false
	for _, r := range line {
		switch {
		case escaped:
			cell.WriteRune(r)
			escaped = false
		case r == '\\':
			escaped = true
		case r == '`':
			inCode = !inCode
			cell.WriteRune(r)
		case r == '|' && !inCode:
			cells = append(cells, strings.TrimSpace(cell.String()))
			cell.Reset()
		default:
			cell.WriteRune(r)
		}
	}
	if escaped {
		cell.WriteRune('\\')
	}
	cells = append(cells, strings.TrimSpace(cell.String()))
	return cells, true
}

func markdownTableWidths(rows [][]string, width int) []int {
	columns := len(rows[0])
	available := width - 3*columns - 1
	if available < columns {
		return nil
	}
	desired := make([]int, columns)
	for _, row := range rows {
		for column, cell := range row {
			desired[column] = max(desired[column], markdownANSIWidth(strings.TrimSpace(cell)))
		}
	}
	widths := make([]int, columns)
	for index := range widths {
		widths[index] = 1
	}
	for remaining := available - columns; remaining > 0; {
		grew := false
		for column := range widths {
			if remaining == 0 {
				break
			}
			if widths[column] < max(desired[column], 1) {
				widths[column]++
				remaining--
				grew = true
			}
		}
		if !grew {
			break
		}
	}
	return widths
}

func markdownANSIWidth(value string) int {
	for _, sequence := range []string{ansiReset, ansiBold, ansiDim, ansiItalic, ansiUnderline, ansiCyan, ansiYellow} {
		value = strings.ReplaceAll(value, sequence, "")
	}
	width := 0
	for _, r := range value {
		width += runeWidth(r)
	}
	return width
}

func markdownLine(raw string) []markdownSpan {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	if strings.Trim(trimmed, "-_*") == "" && len(trimmed) >= 3 {
		return []markdownSpan{{text: strings.Repeat("─", min(40, len([]rune(trimmed)))), style: ansiDim}}
	}
	style := ""
	for level := 6; level >= 1; level-- {
		prefix := strings.Repeat("#", level) + " "
		if strings.HasPrefix(trimmed, prefix) {
			trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, prefix))
			style = ansiBold + ansiCyan
			break
		}
	}
	if strings.HasPrefix(trimmed, "> ") {
		trimmed = "│ " + strings.TrimSpace(strings.TrimPrefix(trimmed, "> "))
		style += ansiDim
	} else if strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ") || strings.HasPrefix(trimmed, "+ ") {
		trimmed = "• " + strings.TrimSpace(trimmed[2:])
		style += ansiYellow
	}
	spans := inlineMarkdown(trimmed)
	if style != "" {
		for index := range spans {
			spans[index].style = style + spans[index].style
		}
	}
	return spans
}

func inlineMarkdown(value string) []markdownSpan {
	var spans []markdownSpan
	for len(value) > 0 {
		if strings.HasPrefix(value, "**") {
			if end := strings.Index(value[2:], "**"); end >= 0 {
				spans = append(spans, markdownSpan{text: value[2 : end+2], style: ansiBold})
				value = value[end+4:]
				continue
			}
			spans = append(spans, markdownSpan{text: value})
			break
		}
		if value[0] == '`' {
			if end := strings.IndexByte(value[1:], '`'); end >= 0 {
				spans = append(spans, markdownSpan{text: value[1 : end+1], style: ansiCyan})
				value = value[end+2:]
				continue
			}
		}
		if value[0] == '[' {
			closeLabel := strings.Index(value, "](")
			if closeLabel > 0 {
				if closeURL := strings.IndexByte(value[closeLabel+2:], ')'); closeURL >= 0 {
					urlEnd := closeLabel + 2 + closeURL
					spans = append(spans,
						markdownSpan{text: value[1:closeLabel], style: ansiUnderline},
						markdownSpan{text: " (" + value[closeLabel+2:urlEnd] + ")", style: ansiDim},
					)
					value = value[urlEnd+1:]
					continue
				}
			}
		}
		if value[0] == '*' || value[0] == '_' {
			marker := value[0]
			if end := strings.IndexByte(value[1:], marker); end >= 0 {
				spans = append(spans, markdownSpan{text: value[1 : end+1], style: ansiItalic})
				value = value[end+2:]
				continue
			}
		}
		next := nextMarkdownMarker(value)
		spans = append(spans, markdownSpan{text: value[:next]})
		value = value[next:]
	}
	return spans
}

func nextMarkdownMarker(value string) int {
	for index := 1; index < len(value); index++ {
		if value[index] == '*' || value[index] == '_' || value[index] == '`' || value[index] == '[' {
			return index
		}
	}
	return len(value)
}

func wrapMarkdownSpans(spans []markdownSpan, width int) []string {
	var lines []string
	var line strings.Builder
	used := 0
	flush := func() {
		lines = append(lines, line.String())
		line.Reset()
		used = 0
	}
	for _, span := range spans {
		for len(span.text) > 0 {
			if used == width {
				flush()
			}
			available := width - used
			end, takenWidth := markdownPrefixWidth(span.text, available)
			if end == 0 {
				flush()
				continue
			}
			part := span.text[:end]
			if span.style != "" {
				line.WriteString(span.style)
				line.WriteString(part)
				line.WriteString(ansiReset)
			} else {
				line.WriteString(part)
			}
			used += takenWidth
			span.text = span.text[end:]
		}
	}
	if line.Len() > 0 || len(lines) == 0 {
		flush()
	}
	return lines
}

func markdownPrefixWidth(value string, maximum int) (int, int) {
	end, width := 0, 0
	for end < len(value) {
		r, size := utf8.DecodeRuneInString(value[end:])
		next := width + runeWidth(r)
		if next > maximum {
			if end == 0 {
				return size, next
			}
			break
		}
		end += size
		width = next
	}
	return end, width
}

func runeWidth(r rune) int {
	if r == 0 || r < 32 || r >= 0x7f && r < 0xa0 || unicode.Is(unicode.Mn, r) {
		return 0
	}
	if r >= 0x1100 && (r <= 0x115f || r == 0x2329 || r == 0x232a || r >= 0x2e80 && r <= 0xa4cf || r >= 0xac00 && r <= 0xd7a3 || r >= 0xf900 && r <= 0xfaff || r >= 0xfe10 && r <= 0xfe6f || r >= 0xff00 && r <= 0xff60 || r >= 0xffe0 && r <= 0xffe6 || r >= 0x1f300 && r <= 0x1faff) {
		return 2
	}
	return 1
}
