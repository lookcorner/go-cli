package tui

import (
	"fmt"
	"regexp"
	"strings"
)

const (
	ansiSearchMatch = "\x1b[30;43m"
	ansiSearchOther = "\x1b[7m"
)

type scrollSearchMatch struct {
	line       int
	start, end int
}

type scrollSearchState struct {
	query     []rune
	cursor    int
	composing bool
	matches   []scrollSearchMatch
	current   int
	err       error
}

func newScrollSearch() *scrollSearchState {
	return &scrollSearchState{composing: true, current: -1}
}

func (s *scrollSearchState) queryText() string {
	return string(s.query)
}

func (s *scrollSearchState) update(lines []string) {
	s.matches = nil
	s.current = -1
	s.err = nil
	if len(s.query) == 0 {
		return
	}
	pattern, err := regexp.Compile(s.queryText())
	if err != nil {
		s.err = err
		return
	}
	for lineIndex, line := range lines {
		for _, match := range pattern.FindAllStringIndex(line, -1) {
			s.matches = append(s.matches, scrollSearchMatch{line: lineIndex, start: match[0], end: match[1]})
		}
	}
	if len(s.matches) > 0 {
		s.current = 0
	}
}

func (s *scrollSearchState) step(delta int) {
	if len(s.matches) == 0 {
		return
	}
	s.current = (s.current + delta) % len(s.matches)
	if s.current < 0 {
		s.current += len(s.matches)
	}
}

func (s *scrollSearchState) status() string {
	if s.err != nil {
		return "search: invalid pattern"
	}
	if len(s.query) == 0 {
		return "search: type a pattern"
	}
	position := 0
	if s.current >= 0 {
		position = s.current + 1
	}
	mode := "n/N next/prev · Esc close"
	if s.composing {
		mode = "Enter accept · Esc cancel"
	}
	return fmt.Sprintf("search /%s/ %d/%d · %s", s.queryText(), position, len(s.matches), mode)
}

func (s *scrollSearchState) highlighted(lines []string) []string {
	if len(s.matches) == 0 {
		return lines
	}
	result := append([]string(nil), lines...)
	byLine := make(map[int][]int)
	for index, match := range s.matches {
		if match.line < 0 || match.line >= len(lines) {
			continue
		}
		byLine[match.line] = append(byLine[match.line], index)
	}
	for line, indices := range byLine {
		plain := stripUIANSI(lines[line])
		var rendered strings.Builder
		position := 0
		for _, matchIndex := range indices {
			match := s.matches[matchIndex]
			style := ansiSearchOther
			if matchIndex == s.current {
				style = ansiSearchMatch
			}
			if match.start < position || match.end > len(plain) || match.start == match.end {
				continue
			}
			rendered.WriteString(plain[position:match.start])
			rendered.WriteString(style)
			rendered.WriteString(plain[match.start:match.end])
			rendered.WriteString(ansiReset)
			position = match.end
		}
		rendered.WriteString(plain[position:])
		result[line] = rendered.String()
	}
	return result
}
