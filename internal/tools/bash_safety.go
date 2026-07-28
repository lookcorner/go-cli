package tools

import (
	"regexp"
	"strings"
)

var (
	fullProcessMatch = regexp.MustCompile(`(?m)(?:^|[;&|(\n])\s*(pkill|pgrep)((?:\s+-[A-Za-z]*f[A-Za-z]*|\s+--full\b)+)\s+(?:'([^']*)'|"([^"]*)"|([^\s;&|()]+))`)
	killCommand      = regexp.MustCompile(`(?:^|[\s;&|()])kill(?:\s|$)`)
)

type selfMatchingProcessKill struct {
	command string
	pattern string
}

func findSelfMatchingProcessKill(command string) (selfMatchingProcessKill, bool) {
	for _, match := range fullProcessMatch.FindAllStringSubmatchIndex(command, -1) {
		processCommand := command[match[2]:match[3]]
		pattern := firstRegexpGroup(command, match, 3, 4, 5)
		if len(pattern) < 3 || strings.Contains(pattern, "$(") || strings.Contains(pattern, "$`") || strings.HasPrefix(pattern, "`") || strings.Contains(pattern, "${") {
			continue
		}
		rest := command[:match[0]] + "\n" + command[match[1]:]
		if processCommand == "pgrep" && !killCommand.MatchString(rest) {
			continue
		}
		if strings.Contains(rest, pattern) {
			return selfMatchingProcessKill{command: processCommand, pattern: pattern}, true
		}
	}
	return selfMatchingProcessKill{}, false
}

func firstRegexpGroup(value string, indexes []int, groups ...int) string {
	for _, group := range groups {
		start, end := indexes[group*2], indexes[group*2+1]
		if start >= 0 {
			return value[start:end]
		}
	}
	return ""
}
