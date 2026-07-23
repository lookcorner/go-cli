package tui

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
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
	link  string
}

var bareHyperlinkPattern = regexp.MustCompile(`(?i)(?:https?://[^\s\x00-\x1f]+|ftp://[^\s\x00-\x1f]+|mailto:[^\s\x00-\x1f]+)`)

func renderMarkdown(value string, width int) []string {
	return renderMarkdownTheme(value, width, false, paletteFor("groknight"))
}

func renderMarkdownWithLinks(value string, width int, links bool) []string {
	return renderMarkdownTheme(value, width, links, paletteFor("groknight"))
}

func renderMarkdownTheme(value string, width int, links bool, theme themePalette) []string {
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
				lines = append(lines, wrapMarkdownSpans([]markdownSpan{{text: language, style: ansiDim}}, width, links)...)
			}
			continue
		}
		if inCode {
			lines = append(lines, wrapMarkdownSpans([]markdownSpan{{text: "  " + raw, style: theme.code}}, width, links)...)
			continue
		}
		if index+1 < len(rawLines) {
			if table, consumed := renderMarkdownTable(rawLines[index:], width, links, theme); consumed > 0 {
				lines = append(lines, table...)
				index += consumed - 1
				continue
			}
		}
		spans := markdownLine(raw, theme)
		if len(spans) == 0 {
			lines = append(lines, "")
			continue
		}
		lines = append(lines, wrapMarkdownSpans(spans, width, links)...)
	}
	return lines
}

func renderMarkdownTable(lines []string, width int, links bool, theme themePalette) ([]string, int) {
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
			wrapped[column] = wrapMarkdownSpans(inlineMarkdown(strings.TrimSpace(cell), theme), columnWidths[column], links)
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
	value = ansi.Strip(value)
	width := 0
	for _, r := range value {
		width += runeWidth(r)
	}
	return width
}

func markdownLine(raw string, theme themePalette) []markdownSpan {
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
			style = ansiBold + theme.heading
			break
		}
	}
	if strings.HasPrefix(trimmed, "> ") {
		trimmed = "│ " + strings.TrimSpace(strings.TrimPrefix(trimmed, "> "))
		style += ansiDim
	} else if strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ") || strings.HasPrefix(trimmed, "+ ") {
		trimmed = "• " + strings.TrimSpace(trimmed[2:])
		style += theme.list
	} else if marker, content, ok := orderedListItem(trimmed); ok {
		trimmed = marker + " " + content
		style += theme.list
	}
	spans := inlineMarkdown(trimmed, theme)
	if style != "" {
		for index := range spans {
			spans[index].style = style + spans[index].style
		}
	}
	return spans
}

func orderedListItem(value string) (string, string, bool) {
	end := 0
	for end < len(value) && end < 9 && value[end] >= '0' && value[end] <= '9' {
		end++
	}
	if end == 0 || end+1 >= len(value) || (value[end] != '.' && value[end] != ')') || value[end+1] != ' ' {
		return "", "", false
	}
	return value[:end+1], strings.TrimSpace(value[end+2:]), true
}

func inlineMarkdown(value string, theme themePalette) []markdownSpan {
	var spans []markdownSpan
	for len(value) > 0 {
		if value[0] == '"' || value[0] == '\'' {
			quote := value[0]
			if end := strings.IndexByte(value[1:], quote); end >= 0 {
				path := value[1 : end+1]
				if isAbsoluteDisplayPath(path) {
					spans = append(spans,
						markdownSpan{text: string(quote)},
						markdownSpan{text: path, link: fileHyperlink(path)},
						markdownSpan{text: string(quote)},
					)
					value = value[end+2:]
					continue
				}
			}
		}
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
				spans = append(spans, markdownSpan{text: value[1 : end+1], style: theme.code})
				value = value[end+2:]
				continue
			}
		}
		if value[0] == '[' {
			closeLabel := strings.Index(value, "](")
			if closeLabel > 0 {
				if closeURL := markdownLinkURLEnd(value, closeLabel+2); closeURL >= 0 {
					urlEnd := closeLabel + 2 + closeURL
					link := safeHyperlinkTarget(value[closeLabel+2 : urlEnd])
					spans = append(spans,
						markdownSpan{text: value[1:closeLabel], style: ansiUnderline, link: link},
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
		spans = append(spans, linkifyBareHyperlinks(value[:next])...)
		value = value[next:]
	}
	return spans
}

func markdownLinkURLEnd(value string, start int) int {
	depth := 0
	for index := start; index < len(value); index++ {
		switch value[index] {
		case '(':
			depth++
		case ')':
			if depth == 0 {
				return index - start
			}
			depth--
		}
	}
	return -1
}

func linkifyBareHyperlinks(value string) []markdownSpan {
	matches := bareHyperlinkPattern.FindAllStringIndex(value, -1)
	if len(matches) == 0 {
		return []markdownSpan{{text: value}}
	}
	spans := make([]markdownSpan, 0, len(matches)*2+1)
	position := 0
	for _, match := range matches {
		if match[0] > position {
			spans = append(spans, markdownSpan{text: value[position:match[0]]})
		}
		raw := value[match[0]:match[1]]
		linked := stripTrailingURLPunctuation(raw)
		if target := safeHyperlinkTarget(linked); target != "" {
			spans = append(spans, markdownSpan{text: linked, link: target})
		} else {
			spans = append(spans, markdownSpan{text: linked})
		}
		if len(linked) < len(raw) {
			spans = append(spans, markdownSpan{text: raw[len(linked):]})
		}
		position = match[1]
	}
	if position < len(value) {
		spans = append(spans, markdownSpan{text: value[position:]})
	}
	return spans
}

func nextMarkdownMarker(value string) int {
	links := bareHyperlinkPattern.FindAllStringIndex(value, -1)

scan:
	for index := 1; index < len(value); index++ {
		if value[index] == '*' || value[index] == '_' || value[index] == '`' || value[index] == '[' || value[index] == '"' || value[index] == '\'' {
			for _, link := range links {
				if index >= link[0] && index < link[1] {
					index = link[1] - 1
					continue scan
				}
			}
			return index
		}
	}
	return len(value)
}

func wrapMarkdownSpans(spans []markdownSpan, width int, links bool) []string {
	var lines []string
	var line strings.Builder
	used := 0
	flush := func() {
		lines = append(lines, line.String())
		line.Reset()
		used = 0
	}
	for _, span := range spans {
		span.text = sanitizeTerminalText(span.text)
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
			if links && span.link != "" {
				line.WriteString(ansi.SetHyperlink(span.link, "id="+hyperlinkID(span.link)))
			}
			if span.style != "" {
				line.WriteString(span.style)
				line.WriteString(part)
				line.WriteString(ansiReset)
			} else {
				line.WriteString(part)
			}
			if links && span.link != "" {
				line.WriteString(ansi.ResetHyperlink())
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

func sanitizeTerminalText(value string) string {
	return strings.Map(func(char rune) rune {
		switch {
		case char == '\n':
			return char
		case char == '\t' || char < 0x20 || char == 0x7f || char >= 0x80 && char <= 0x9f:
			return ' '
		default:
			return char
		}
	}, value)
}

func isAbsoluteDisplayPath(path string) bool {
	if strings.HasPrefix(path, "/") {
		return true
	}
	return len(path) >= 3 && ((path[0] >= 'A' && path[0] <= 'Z') || (path[0] >= 'a' && path[0] <= 'z')) && path[1] == ':' && (path[2] == '\\' || path[2] == '/')
}

func fileHyperlink(path string) string {
	if len(path) >= 3 && path[1] == ':' {
		path = "/" + strings.ReplaceAll(path, "\\", "/")
	}
	return (&url.URL{Scheme: "file", Path: path}).String()
}

func hyperlinkID(target string) string {
	sum := sha256.Sum256([]byte(target))
	return hex.EncodeToString(sum[:8])
}

func safeHyperlinkTarget(value string) string {
	for _, char := range value {
		if char < 0x20 || char == 0x7f {
			return ""
		}
	}
	target, err := url.Parse(value)
	if err != nil {
		return ""
	}
	switch strings.ToLower(target.Scheme) {
	case "http", "https", "ftp":
		if target.Host == "" {
			return ""
		}
	case "mailto":
		if target.Opaque == "" {
			return ""
		}
	default:
		return ""
	}
	return target.String()
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
