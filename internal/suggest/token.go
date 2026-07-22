package suggest

import "strings"

type quoteStyle byte

const (
	quoteNone quoteStyle = iota
	quoteDouble
	quoteSingle
)

type shellToken struct {
	start         int
	value         string
	dirValueLen   int
	dirRawEnd     int
	quote         quoteStyle
	openQuote     int
	closedQuote   quoteStyle
	closedReopen  bool
	plain         []bool
	tokensBefore  int
	command       string
	afterRedirect bool
}

type tokenBuild struct {
	start, dirValueLen, dirRawEnd int
	value                         strings.Builder
	plain                         []bool
	afterRedirect                 bool
	lastOpen, lastClose           int
	lastStyle                     quoteStyle
}

func (t *tokenBuild) push(index int, r rune, plain bool) {
	t.value.WriteRune(r)
	for range len(string(r)) {
		t.plain = append(t.plain, plain)
	}
	if r == '/' {
		t.dirValueLen = t.value.Len()
		t.dirRawEnd = index + 1
	}
}

func parseToken(prefix string) shellToken {
	var current *tokenBuild
	quote, openQuote, escape := quoteNone, 0, false
	tokensBefore, command, pendingRedirect := 0, "", false
	ensure := func(index int) *tokenBuild {
		if current == nil {
			current = &tokenBuild{start: index, dirRawEnd: index, afterRedirect: pendingRedirect, lastOpen: -1, lastClose: -1}
			pendingRedirect = false
		}
		return current
	}
	finish := func() {
		if current == nil {
			return
		}
		tokensBefore++
		if command == "" && !current.afterRedirect {
			command = current.value.String()
		}
		current = nil
	}

	for index, r := range prefix {
		if escape {
			escape = false
			t := ensure(index)
			if quote == quoteDouble && !strings.ContainsRune("\"\\$`", r) {
				t.push(index, '\\', false)
			}
			t.push(index, r, false)
			continue
		}
		switch quote {
		case quoteSingle:
			if r == '\'' {
				quote = quoteNone
				current.lastOpen, current.lastClose, current.lastStyle = openQuote, index, quoteSingle
			} else {
				ensure(index).push(index, r, false)
			}
		case quoteDouble:
			switch r {
			case '"':
				quote = quoteNone
				current.lastOpen, current.lastClose, current.lastStyle = openQuote, index, quoteDouble
			case '\\':
				escape = true
			default:
				ensure(index).push(index, r, false)
			}
		default:
			switch {
			case r == '\\':
				ensure(index)
				escape = true
			case r == '\'' || r == '"':
				ensure(index)
				if r == '\'' {
					quote = quoteSingle
				} else {
					quote = quoteDouble
				}
				openQuote = index
			case r == '|' || r == ';' || r == '&':
				finish()
				tokensBefore, command, pendingRedirect = 0, "", false
			case r == '<' || r == '>':
				finish()
				pendingRedirect = true
			case r == ' ' || r == '\t' || r == '\n' || r == '\r':
				finish()
			default:
				ensure(index).push(index, r, true)
			}
		}
	}
	if current == nil {
		return shellToken{start: len(prefix), dirValueLen: -1, dirRawEnd: len(prefix), quote: quote, openQuote: openQuote, tokensBefore: tokensBefore, command: command, afterRedirect: pendingRedirect}
	}
	closed, reopen := quoteNone, false
	if current.lastClose >= current.dirRawEnd {
		closed, reopen = current.lastStyle, current.lastOpen >= current.dirRawEnd
	}
	dirValueLen := current.dirValueLen
	if dirValueLen == 0 {
		dirValueLen = -1
	}
	return shellToken{start: current.start, value: current.value.String(), dirValueLen: dirValueLen, dirRawEnd: current.dirRawEnd, quote: quote, openQuote: openQuote, closedQuote: closed, closedReopen: reopen, plain: current.plain, tokensBefore: tokensBefore, command: command, afterRedirect: current.afterRedirect}
}

func buildToken(tok shellToken, rawDir, name string, directory bool) string {
	var out strings.Builder
	out.WriteString(rawDir)
	if rawDir == "" && strings.HasPrefix(name, "-") {
		out.WriteString("./")
	}
	style, reopen := tok.quote, tok.openQuote >= tok.dirRawEnd
	if style == quoteNone {
		style, reopen = tok.closedQuote, tok.closedReopen
	}
	if reopen {
		if style == quoteDouble {
			out.WriteByte('"')
		} else if style == quoteSingle {
			out.WriteByte('\'')
		}
	}
	switch style {
	case quoteDouble:
		for _, r := range name {
			if strings.ContainsRune("\"\\$`", r) {
				out.WriteByte('\\')
			}
			out.WriteRune(r)
		}
	case quoteSingle:
		out.WriteString(strings.ReplaceAll(name, "'", "'\\''"))
	default:
		if strings.IndexFunc(name, func(r rune) bool { return r < 32 || r == 127 }) >= 0 {
			out.WriteByte('\'')
			out.WriteString(strings.ReplaceAll(name, "'", "'\\''"))
			out.WriteByte('\'')
		} else {
			for _, r := range name {
				if strings.ContainsRune(" \t\"'\\$`&|;()<>*?[]#!{}~", r) {
					out.WriteByte('\\')
				}
				out.WriteRune(r)
			}
		}
	}
	if directory {
		out.WriteByte('/')
	} else if style == quoteDouble {
		out.WriteByte('"')
	} else if style == quoteSingle {
		out.WriteByte('\'')
	}
	return out.String()
}
