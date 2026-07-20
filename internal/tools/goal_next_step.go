package tools

import (
	"bytes"
	"io"
	"os"
	"strings"
)

const (
	goalPlanReadBytes    = 8 << 10
	goalNextStepMaxChars = 400
)

func (r *Registry) GoalNextStep() string {
	if r == nil || r.goal == nil {
		return ""
	}
	return firstGoalPlanStep(r.goal.Snapshot().PlanPath)
}

func firstGoalPlanStep(path string) string {
	if path == "" {
		return ""
	}
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, goalPlanReadBytes))
	if err != nil {
		return ""
	}
	if len(data) >= goalPlanReadBytes {
		if newline := bytes.LastIndexByte(data, '\n'); newline >= 0 {
			data = data[:newline]
		}
	}
	body := strings.ToValidUTF8(string(data), "\uFFFD")
	step := firstUncheckedGoalItem(body)
	if step == "" {
		return ""
	}
	step = capGoalStep(step)
	return sanitizeGoalDirective(step, 0)
}

var goalDirectiveSanitizer = strings.NewReplacer(
	"</system-reminder>", "<\u200b/system-reminder>",
	"<system-reminder>", "<\u200bsystem-reminder>",
	"</goal-state>", "<\u200b/goal-state>",
	"<goal-state>", "<\u200bgoal-state>",
	"{", "{\u200b", "}", "\u200b}",
)

func sanitizeGoalDirective(value string, maxChars int) string {
	value = strings.TrimSpace(value)
	if maxChars > 0 {
		runes := []rune(value)
		if len(runes) > maxChars {
			value = string(runes[:maxChars]) + "\u2026"
		}
	}
	return goalDirectiveSanitizer.Replace(value)
}

func firstUncheckedGoalItem(body string) string {
	lines := strings.Split(body, "\n")
	checklistLevel := 0
	for _, line := range lines {
		if goalHeaderName(line) == "task checklist" {
			checklistLevel = goalHeaderLevel(line)
			continue
		}
		if checklistLevel == 0 {
			continue
		}
		if level := goalHeaderLevel(line); level > 0 && level <= checklistLevel {
			return ""
		}
		if item := uncheckedGoalItem(line); item != "" {
			return item
		}
	}
	if checklistLevel > 0 {
		return ""
	}

	excluded := false
	for _, line := range lines {
		if name := goalHeaderName(line); name != "" {
			excluded = name == "non-goals" || name == "deviations"
			continue
		}
		if !excluded {
			if item := uncheckedGoalItem(line); item != "" {
				return item
			}
		}
	}
	return ""
}

func uncheckedGoalItem(line string) string {
	line = strings.TrimSpace(line)
	for _, marker := range []string{"- ", "* ", "+ "} {
		if rest, ok := strings.CutPrefix(line, marker); ok {
			if item, ok := strings.CutPrefix(strings.TrimSpace(rest), "[ ]"); ok {
				return strings.TrimSpace(item)
			}
			return ""
		}
	}
	return ""
}

func goalHeaderName(line string) string {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "#") {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(strings.TrimLeft(line, "#")))
}

func goalHeaderLevel(line string) int {
	line = strings.TrimSpace(line)
	return len(line) - len(strings.TrimLeft(line, "#"))
}

func capGoalStep(step string) string {
	runes := []rune(step)
	if len(runes) <= goalNextStepMaxChars {
		return step
	}
	return string(runes[:goalNextStepMaxChars]) + "\u2026"
}
