package agent

import (
	"strings"
	"testing"
)

func TestParsePrivacyCommand(t *testing.T) {
	tests := []struct {
		prompt, want, status string
		error                bool
		ok                   bool
	}{
		{"/privacy", "Product: Gork Build", "privacy status", false, true},
		{"  /privacy  ", "coding data retention opted out", "privacy status", false, true},
		{"/privacy opt-out", "Coding data sharing: Opt out", "privacy opt-out", false, true},
		{"/privacy OUT", "Coding data sharing: Opt out", "privacy opt-out", false, true},
		{"/privacy Private", "Coding data sharing: Opt out", "privacy opt-out", false, true},
		{"/privacy opt-in", PrivacyLockedMessage, "privacy locked", true, true},
		{"/privacy IN", PrivacyLockedMessage, "privacy locked", true, true},
		{"/privacy Share", PrivacyLockedMessage, "privacy locked", true, true},
		{"/privacy on", "Unknown argument", "privacy argument invalid", true, true},
		{"/privacy false", "Unknown argument", "privacy argument invalid", true, true},
		{"/privacy something else", "Unknown argument", "privacy argument invalid", true, true},
		{"/privacyx", "", "", false, false},
		{"privacy", "", "", false, false},
		{"", "", "", false, false},
	}
	for _, test := range tests {
		t.Run(test.prompt, func(t *testing.T) {
			result, ok := ParsePrivacyCommand(test.prompt)
			if ok != test.ok || result.Status != test.status || result.Error != test.error || !strings.Contains(result.Message, test.want) {
				t.Fatalf("result=%#v ok=%v", result, ok)
			}
		})
	}
}

func TestPrivacyUnknownArgumentListsOnlyUnambiguousAliases(t *testing.T) {
	result, ok := ParsePrivacyCommand("/privacy garbage")
	if !ok || !result.Error {
		t.Fatalf("result=%#v ok=%v", result, ok)
	}
	for _, alias := range []string{"opt-in", "in", "share", "opt-out", "out", "private"} {
		if !strings.Contains(result.Message, alias) {
			t.Errorf("missing alias %q in %q", alias, result.Message)
		}
	}
	for _, ambiguous := range []string{" on", " off", "true", "false", "enable", "disable"} {
		if strings.Contains(result.Message, ambiguous) {
			t.Errorf("ambiguous alias %q appears in %q", ambiguous, result.Message)
		}
	}
}
