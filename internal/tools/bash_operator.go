package tools

import "strings"

type heredocDelimiter struct {
	value     string
	stripTabs bool
}

func containsUnwaitedBackgroundOperator(command string) bool {
	return containsBackgroundOperator(command) && !endsWithWaitBuiltin(command)
}

func endsWithWaitBuiltin(command string) bool {
	trimmed := strings.TrimSpace(command)
	trimmed = strings.TrimSpace(strings.TrimRight(trimmed, ";\n"))
	return trimmed == "wait" || strings.HasSuffix(trimmed, " wait") || strings.HasSuffix(trimmed, ";wait") || strings.HasSuffix(trimmed, "\twait") || strings.HasSuffix(trimmed, "\nwait")
}

func containsBackgroundOperator(command string) bool {
	pending := make([]heredocDelimiter, 0, 1)
	inSingle, inDouble := false, false
	for i := 0; i < len(command); {
		switch command[i] {
		case '\\':
			if !inSingle {
				i += min(2, len(command)-i)
				continue
			}
		case '\'':
			if !inDouble {
				inSingle = !inSingle
				i++
				continue
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
				i++
				continue
			}
		case '\n':
			if !inSingle && !inDouble && len(pending) > 0 {
				i++
				for _, delimiter := range pending {
					i = skipHeredocBody(command, i, delimiter)
				}
				pending = pending[:0]
				continue
			}
		case '<':
			if !inSingle && !inDouble && i+1 < len(command) && command[i+1] == '<' && (i+2 >= len(command) || command[i+2] != '<') {
				if delimiter, after, ok := parseHeredocStart(command, i); ok {
					pending = append(pending, delimiter)
					i = after
					continue
				}
			}
		case '&':
			if !inSingle && !inDouble {
				if i+1 < len(command) && command[i+1] == '&' {
					i += 2
					continue
				}
				if i+1 < len(command) && command[i+1] == '>' {
					i += 2
					if i < len(command) && command[i] == '>' {
						i++
					}
					continue
				}
				if i > 0 && (command[i-1] == '>' || command[i-1] == '<') {
					i++
					continue
				}
				return true
			}
		}
		i++
	}
	return false
}

func parseHeredocStart(command string, start int) (heredocDelimiter, int, bool) {
	i := start + 2
	delimiter := heredocDelimiter{}
	if i < len(command) && command[i] == '-' {
		delimiter.stripTabs = true
		i++
	}
	for i < len(command) && (command[i] == ' ' || command[i] == '\t') {
		i++
	}
	if i >= len(command) || command[i] == '\n' {
		return delimiter, start, false
	}
	if command[i] == '\'' || command[i] == '"' {
		quote := command[i]
		i++
		wordStart := i
		for i < len(command) && command[i] != quote {
			i++
		}
		if i >= len(command) {
			return delimiter, start, false
		}
		delimiter.value = command[wordStart:i]
		i++
	} else {
		wordStart := i
		for i < len(command) && !isShellSpace(command[i]) && !strings.ContainsRune(";&|()<>	", rune(command[i])) {
			i++
		}
		if i == wordStart {
			return delimiter, start, false
		}
		delimiter.value = strings.ReplaceAll(command[wordStart:i], "\\", "")
	}
	return delimiter, i, delimiter.value != ""
}

func skipHeredocBody(command string, start int, delimiter heredocDelimiter) int {
	for start < len(command) {
		end := strings.IndexByte(command[start:], '\n')
		if end < 0 {
			end = len(command)
		} else {
			end += start
		}
		line := command[start:end]
		if delimiter.stripTabs {
			line = strings.TrimLeft(line, "\t")
		}
		if line == delimiter.value {
			return min(end+1, len(command))
		}
		if end == len(command) {
			return end
		}
		start = end + 1
	}
	return len(command)
}

func isShellSpace(value byte) bool {
	return value == ' ' || value == '\t' || value == '\r' || value == '\n'
}
