package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

func TestSmallScreenBandBoundaries(t *testing.T) {
	for _, rows := range []int{0, 1, 20, 29, 40} {
		if smallScreenBandContains(rows) {
			t.Fatalf("rows=%d unexpectedly in band", rows)
		}
	}
	for _, rows := range []int{21, 24, 28} {
		if !smallScreenBandContains(rows) {
			t.Fatalf("rows=%d missing from band", rows)
		}
	}
}

func TestSmallScreenHintWaitsForMeasuredSizeAndShowsOnce(t *testing.T) {
	m := &model{
		width: 80, height: 24, status: "ready",
		smallScreenHint: smallScreenHintState{enabled: true},
	}
	updated, _ := m.Update(statusEvent{text: "ready"})
	m = updated.(*model)
	if m.smallScreenHint.evaluated || m.smallScreenHint.active {
		t.Fatalf("default size was treated as measured: %#v", m.smallScreenHint)
	}

	updated, command := m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m = updated.(*model)
	if command == nil || !m.smallScreenHint.evaluated || !m.smallScreenHint.active ||
		m.smallScreenHint.remaining != 3*time.Second {
		t.Fatalf("command=%v hint=%#v", command != nil, m.smallScreenHint)
	}
	if view := stripUIANSI(m.View().Content); !strings.Contains(view, "Tight on space? Try /compact-mode") {
		t.Fatalf("view=%q", view)
	}

	m.smallScreenHint.active = false
	updated, command = m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = updated.(*model)
	if command != nil || m.smallScreenHint.active {
		t.Fatalf("later resize retriggered hint: command=%v hint=%#v", command != nil, m.smallScreenHint)
	}
}

func TestSmallScreenHintOutOfBandCompactAndDisabledConsumeEvaluation(t *testing.T) {
	for _, test := range []struct {
		name    string
		rows    int
		enabled bool
		compact bool
	}{
		{name: "too short", rows: 20, enabled: true},
		{name: "too tall", rows: 29, enabled: true},
		{name: "compact enabled", rows: 24, enabled: true, compact: true},
		{name: "hint disabled", rows: 24},
	} {
		t.Run(test.name, func(t *testing.T) {
			m := &model{
				compactMode:     test.compact,
				smallScreenHint: smallScreenHintState{enabled: test.enabled},
			}
			updated, command := m.Update(tea.WindowSizeMsg{Width: 100, Height: test.rows})
			m = updated.(*model)
			if command != nil || !m.smallScreenHint.evaluated || m.smallScreenHint.active {
				t.Fatalf("command=%v hint=%#v", command != nil, m.smallScreenHint)
			}
			updated, command = m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
			m = updated.(*model)
			if command != nil || m.smallScreenHint.active {
				t.Fatalf("later resize retriggered hint: command=%v hint=%#v", command != nil, m.smallScreenHint)
			}
		})
	}
}

func TestSmallScreenHintDefersAndFreezesWhileOccluded(t *testing.T) {
	m := &model{
		settings:        &settingsState{},
		smallScreenHint: smallScreenHintState{enabled: true},
	}
	updated, command := m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m = updated.(*model)
	if command != nil || m.smallScreenHint.evaluated || m.smallScreenHint.active {
		t.Fatalf("occlusion consumed evaluation: command=%v hint=%#v", command != nil, m.smallScreenHint)
	}

	m.settings = nil
	updated, command = m.Update(statusEvent{text: "ready"})
	m = updated.(*model)
	if command == nil || !m.smallScreenHint.evaluated || !m.smallScreenHint.active {
		t.Fatalf("clearing occlusion did not show: command=%v hint=%#v", command != nil, m.smallScreenHint)
	}

	m.settings = &settingsState{}
	remaining := m.smallScreenHint.remaining
	updated, command = m.Update(smallScreenHintTickEvent{nonce: m.smallScreenHint.nonce})
	m = updated.(*model)
	if command == nil || m.smallScreenHint.remaining != remaining {
		t.Fatalf("occluded tick burned TTL: command=%v remaining=%v", command != nil, m.smallScreenHint.remaining)
	}

	m.settings = nil
	updated, command = m.Update(smallScreenHintTickEvent{nonce: m.smallScreenHint.nonce})
	m = updated.(*model)
	if command == nil || m.smallScreenHint.remaining != remaining-100*time.Millisecond {
		t.Fatalf("visible tick did not burn TTL: command=%v remaining=%v", command != nil, m.smallScreenHint.remaining)
	}
}

func TestSmallScreenHintSurvivesStatusChangesAndExpires(t *testing.T) {
	m := &model{
		width: 100, height: 24, status: "ready",
		smallScreenHint: smallScreenHintState{
			enabled: true, measured: true, active: true, evaluated: true,
			remaining: 100 * time.Millisecond, nonce: 7,
		},
	}
	updated, _ := m.Update(statusEvent{text: "thinking"})
	m = updated.(*model)
	if !m.smallScreenHint.active || m.status != "thinking" {
		t.Fatalf("status change retired hint: status=%q hint=%#v", m.status, m.smallScreenHint)
	}
	updated, command := m.Update(smallScreenHintTickEvent{nonce: 7})
	m = updated.(*model)
	if command != nil || m.smallScreenHint.active {
		t.Fatalf("hint did not expire: command=%v hint=%#v", command != nil, m.smallScreenHint)
	}
}
