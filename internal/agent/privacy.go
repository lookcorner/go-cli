package agent

import (
	"fmt"
	"strings"
)

const PrivacyLockedMessage = "Gork Build locks coding data retention to opt-out; opt-in is not available."

const PrivacyInfoMessage = "  Product: Gork Build (privacy fork of Grok Build)\n" +
	"  Client research uploads: disabled (hard-off)\n" +
	"  Product analytics (Mixpanel / events): disabled\n" +
	"  Coding data retention: opt-out only (locked in this build)\n" +
	"  Model inference: required (files the agent reads go to the model API)\n\n" +
	"  Account: privacy mode (coding data retention opted out)\n\n" +
	"  Docs: https://github.com/thedavidweng/gork-build"

type PrivacyResult struct {
	Message string
	Status  string
	Error   bool
}

func ParsePrivacyCommand(prompt string) (PrivacyResult, bool) {
	fields := strings.Fields(prompt)
	if len(fields) == 0 || fields[0] != "/privacy" {
		return PrivacyResult{}, false
	}
	if len(fields) == 1 {
		return PrivacyResult{Message: PrivacyInfoMessage, Status: "privacy status"}, true
	}
	arg := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(prompt), fields[0]))
	switch strings.ToLower(arg) {
	case "opt-out", "out", "private":
		return PrivacyResult{Message: "Coding data sharing: Opt out", Status: "privacy opt-out"}, true
	case "opt-in", "in", "share":
		return PrivacyResult{Message: PrivacyLockedMessage, Status: "privacy locked", Error: true}, true
	default:
		return PrivacyResult{
			Message: fmt.Sprintf("Unknown argument %q. Use /privacy to view status, /privacy opt-out|out|private to confirm opt-out; opt-in|in|share is unavailable.", arg),
			Status:  "privacy argument invalid",
			Error:   true,
		}, true
	}
}
