package tools

import (
	"regexp"
	"strings"
)

type goalStopPattern struct {
	label string
	re    *regexp.Regexp
}

var goalStopPatterns = []goalStopPattern{
	{"unable_to_proceed", regexp.MustCompile(`^I (?:can(?:'?t|not)|am unable to) (?:proceed|continue|make (?:any )?progress|complete|fix this)\b`)},
	{"giving_up", regexp.MustCompile(`^(?:Giving up|I(?:'m| am) giving up|The task is not actionable)\b`)},
	{"stopping_here", regexp.MustCompile(`^(?:Stopping here|I've stopped here|Parked (?:the|this) branch|Paused here)(?:\.|,|;|$| for | \x{2014}| -| until| pending| since| because)`)},
	{"agents_in_flight", regexp.MustCompile(`^(?:(?:\*\*)?[1-9]\d* (?:agent|cron|task|fork|job|worker|PR|check)s? (?:in flight|remaining|active|still (?:running|working)|pending|running|launched)\b|(?:Continuous )?(?:[Ll]oop|[Cc]rons?|[Bb]abysit) (?:active|healthy|continuing|running|will keep|continues)\b|Waiting for (?:the )?(?:agent|cron|task|fork|worker|job|remaining|them)s?\b|Agents? will report back\b|Waiting\.?$)`)},
	{"verdict_line", regexp.MustCompile(`^VERDICT: (?:PASS|FAIL)\b`)},
	{"commit_push_pr", regexp.MustCompile("^(?:Pushed (?:to `|`[0-9a-f]{7,})|Committed as `?[0-9a-f]{7,}\\b|Commit: `?[0-9a-f]{7,}\\b|(?:Opened|Created) PR #?\\d)")},
	{"ready_for_review", regexp.MustCompile(`^Ready (?:for review|to (?:upload|merge|ship|land))\b`)},
	{"please_deflection", regexp.MustCompile("^Please (?:start|run|provide|grant|export|add|install|configure|give me|paste|point me|set (?:the |up |`?[A-Z][A-Z0-9_]+\\b))")},
}

var goalCheckBackPattern = regexp.MustCompile(`^(?:I will|I'll|Will) (?:check back|re-?check|poll|look again|retry|re-?run|try again) (?:in\b|again\b|(?:when|once|after|until)\s+(\S+))`)
var goalParagraphBreak = regexp.MustCompile(`\n[ \t]*\n+`)

func (r *Registry) DetectGoalPrematureStop(text string) string {
	if r == nil || r.goal == nil || r.todos == nil || !r.todos.hasPending() || r.goal.Snapshot().Status != "active" {
		return ""
	}
	pattern := matchedGoalStopPattern(text)
	if pattern != "" {
		r.emitGoalEvent("goal_premature_stop_detected", map[string]any{"pattern": pattern})
		r.emitGoalUpdatedWith("premature_stop_detected", map[string]any{"premature_stop_pattern": pattern})
	}
	return pattern
}

func matchedGoalStopPattern(text string) string {
	text = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n"))
	if text == "" {
		return ""
	}
	paragraphs := goalParagraphBreak.Split(text, -1)
	lines := strings.Split(paragraphs[len(paragraphs)-1], "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		for _, pattern := range goalStopPatterns {
			if pattern.re.MatchString(line) {
				return pattern.label
			}
		}
		if match := goalCheckBackPattern.FindStringSubmatch(line); match != nil && (len(match) < 2 || !goalUserPronoun(match[1])) {
			return "check_back_later"
		}
	}
	return ""
}

func goalUserPronoun(token string) bool {
	token = strings.ToLower(token)
	for _, word := range []string{"your", "you"} {
		if strings.HasPrefix(token, word) {
			rest := token[len(word):]
			return rest == "" || !(rest[0] >= 'a' && rest[0] <= 'z' || rest[0] >= '0' && rest[0] <= '9' || rest[0] == '_')
		}
	}
	return false
}
