package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

func TestSSHWrapHintShowsOnceWhenRecommended(t *testing.T) {
	m := &model{
		status: "ready", sshWrapRecommended: true,
		sshWrapHint: smallScreenHintState{enabled: true},
	}
	updated, command := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(*model)
	if command == nil || !m.sshWrapHint.evaluated || !m.sshWrapHint.active ||
		m.sshWrapHint.remaining != 10*time.Second {
		t.Fatalf("command=%v hint=%#v", command != nil, m.sshWrapHint)
	}
	if content := stripUIANSI(m.View().Content); !strings.Contains(content, "Run /doctor for details and fixes.") {
		t.Fatalf("content=%q", content)
	}

	m.sshWrapHint.active = false
	updated, command = m.Update(statusEvent{text: "ready"})
	m = updated.(*model)
	if m.sshWrapHint.active {
		t.Fatalf("hint retriggered: command=%v hint=%#v", command != nil, m.sshWrapHint)
	}
}

func TestSSHWrapHintConsumesDisabledAndUnsupportedEnvironment(t *testing.T) {
	for _, test := range []struct {
		name        string
		enabled     bool
		recommended bool
	}{
		{name: "disabled", recommended: true},
		{name: "not recommended", enabled: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			m := &model{
				sshWrapRecommended: test.recommended,
				sshWrapHint:        smallScreenHintState{enabled: test.enabled},
			}
			updated, command := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
			m = updated.(*model)
			if command != nil || !m.sshWrapHint.evaluated || m.sshWrapHint.active {
				t.Fatalf("command=%v hint=%#v", command != nil, m.sshWrapHint)
			}
		})
	}
}

func TestSSHWrapHintDefersBehindOtherHintsAndPausesWhenOccluded(t *testing.T) {
	m := &model{
		sshWrapRecommended: true,
		sshWrapHint:        smallScreenHintState{enabled: true},
		smallScreenHint:    smallScreenHintState{active: true},
	}
	updated, command := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(*model)
	if command != nil || m.sshWrapHint.evaluated || m.sshWrapHint.active {
		t.Fatalf("busy hint consumed evaluation: command=%v hint=%#v", command != nil, m.sshWrapHint)
	}

	m.smallScreenHint.active = false
	updated, command = m.Update(statusEvent{text: "ready"})
	m = updated.(*model)
	if command == nil || !m.sshWrapHint.active {
		t.Fatalf("clearing hint did not start SSH hint: command=%v hint=%#v", command != nil, m.sshWrapHint)
	}

	m.settings = &settingsState{}
	remaining := m.sshWrapHint.remaining
	updated, command = m.Update(sshWrapHintTickEvent{nonce: m.sshWrapHint.nonce})
	m = updated.(*model)
	if command == nil || m.sshWrapHint.remaining != remaining {
		t.Fatalf("occluded tick burned TTL: command=%v remaining=%v", command != nil, m.sshWrapHint.remaining)
	}

	m.settings = nil
	updated, command = m.Update(sshWrapHintTickEvent{nonce: m.sshWrapHint.nonce})
	m = updated.(*model)
	if command == nil || m.sshWrapHint.remaining != remaining-100*time.Millisecond {
		t.Fatalf("visible tick did not burn TTL: command=%v remaining=%v", command != nil, m.sshWrapHint.remaining)
	}

	m.sshWrapHint.remaining = 100 * time.Millisecond
	updated, command = m.Update(sshWrapHintTickEvent{nonce: m.sshWrapHint.nonce})
	m = updated.(*model)
	if command != nil || m.sshWrapHint.active {
		t.Fatalf("hint did not expire: command=%v hint=%#v", command != nil, m.sshWrapHint)
	}
}
