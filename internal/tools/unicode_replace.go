package tools

import (
	"strings"
	"unicode/utf8"
)

type normalizedMatch struct {
	start  int
	length int
}

func normalizeTypography(text string) string {
	var output strings.Builder
	output.Grow(len(text))
	for _, char := range text {
		if replacement, ok := typographyReplacement(char); ok {
			output.WriteString(replacement)
		} else {
			output.WriteRune(char)
		}
	}
	return output.String()
}

func typographyReplacement(char rune) (string, bool) {
	switch char {
	case '\u201c', '\u201d':
		return `"`, true
	case '\u2018', '\u2019':
		return "'", true
	case '\u2014':
		return "--", true
	case '\u2013':
		return "-", true
	case '\u2026':
		return "...", true
	case '\u00a0':
		return " ", true
	default:
		return "", false
	}
}

func normalizedTextAndOffsets(text string) (string, []int) {
	var normalized strings.Builder
	normalized.Grow(len(text))
	offsets := make([]int, 0, len(text)+1)
	for byteOffset, char := range text {
		if replacement, ok := typographyReplacement(char); ok {
			for range len(replacement) {
				offsets = append(offsets, byteOffset)
			}
			normalized.WriteString(replacement)
		} else {
			for index := range utf8.RuneLen(char) {
				offsets = append(offsets, byteOffset+index)
			}
			normalized.WriteRune(char)
		}
	}
	offsets = append(offsets, len(text))
	return normalized.String(), offsets
}

func findNormalizedMatches(text, pattern string) ([]normalizedMatch, bool) {
	normalized, offsets := normalizedTextAndOffsets(text)
	pattern = normalizeTypography(pattern)
	if pattern == "" {
		return nil, false
	}
	var matches []normalizedMatch
	rejected := false
	for from := 0; from <= len(normalized)-len(pattern); {
		relative := strings.Index(normalized[from:], pattern)
		if relative < 0 {
			break
		}
		start := from + relative
		end := start + len(pattern)
		originalStart, originalEnd := offsets[start], offsets[end]
		if originalEnd <= originalStart || normalizeTypography(text[originalStart:originalEnd]) != pattern {
			rejected = true
		} else {
			matches = append(matches, normalizedMatch{start: originalStart, length: originalEnd - originalStart})
		}
		from = end
	}
	if len(matches) == 0 {
		return nil, rejected
	}
	for index := 1; index < len(matches); index++ {
		if matches[index-1].start+matches[index-1].length > matches[index].start {
			return nil, true
		}
	}
	return matches, false
}

func replaceNormalizedMatches(text string, matches []normalizedMatch, replacement string) string {
	var output strings.Builder
	last := 0
	for _, match := range matches {
		output.WriteString(text[last:match.start])
		output.WriteString(replacement)
		last = match.start + match.length
	}
	output.WriteString(text[last:])
	return output.String()
}
