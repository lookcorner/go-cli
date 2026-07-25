package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/tools"
	"github.com/lookcorner/go-cli/internal/workspace"
)

func TestPromptMentionsPlanningUsesWholeWords(t *testing.T) {
	for _, text := range []string{
		"let's plan the refactor",
		"Plan it first",
		"some planning before we code",
		"design the system",
		"architect this module",
		"walk me through this step by step",
		"break this down for me",
		"lay out the migration",
		"what's your approach?",
		"pick a strategy",
	} {
		if !promptMentionsPlanning(text) {
			t.Fatalf("planning intent not detected: %q", text)
		}
	}
	for _, text := range []string{
		"explain this code",
		"can you explain the explanation",
		"the planet is round",
		"redesigned the airplane wing",
		"fix the bug in main.go",
		"",
	} {
		if promptMentionsPlanning(text) {
			t.Fatalf("near neighbour matched: %q", text)
		}
	}
}

func TestPlanNudgeFiresOnTypedRisingEdge(t *testing.T) {
	m := planNudgeModel(t)
	for _, char := range "pla" {
		if command := m.editInput(tea.KeyPressMsg(tea.Key{Code: char, Text: string(char)})); command != nil {
			t.Fatalf("partial keyword scheduled a hint: %q", string(m.input))
		}
	}
	command := m.editInput(tea.KeyPressMsg(tea.Key{Code: 'n', Text: "n"}))
	if command == nil || m.status != "Planning? Check out plan mode via shift+tab" || m.planModeHint.shown != 1 {
		t.Fatalf("command=%v status=%q shown=%d", command != nil, m.status, m.planModeHint.shown)
	}
	for _, char := range " the refactor" {
		if command := m.editInput(tea.KeyPressMsg(tea.Key{Code: char, Text: string(char)})); command != nil {
			t.Fatalf("existing keyword refreshed the hint: %q", string(m.input))
		}
	}
}

func TestPlanNudgeSuppressesNonTypedAndUnavailableContexts(t *testing.T) {
	for _, prefix := range []string{"/", "!"} {
		m := planNudgeModel(t)
		for _, char := range prefix + "plan" {
			m.editInput(tea.KeyPressMsg(tea.Key{Code: char, Text: string(char)}))
		}
		if m.planModeHint.shown != 0 {
			t.Fatalf("command draft showed hint: %q", string(m.input))
		}
	}

	for _, configure := range []func(*model){
		func(m *model) { m.planModeHint.enabled = false },
		func(m *model) { m.planMode = true },
		func(m *model) { m.running = true },
		func(m *model) { m.feedbackInput = true },
		func(m *model) { m.rememberInput = true },
		func(m *model) { m.runner = nil },
	} {
		m := planNudgeModel(t)
		configure(m)
		for _, char := range "plan" {
			m.editInput(tea.KeyPressMsg(tea.Key{Code: char, Text: string(char)}))
		}
		if m.planModeHint.shown != 0 {
			t.Fatalf("suppressed context showed hint: %#v", m.planModeHint)
		}
	}

	m := planNudgeModel(t)
	m.setInput("design the api")
	m.editInput(tea.KeyPressMsg(tea.Key{Code: ' ', Text: " "}))
	if m.planModeHint.shown != 0 {
		t.Fatal("programmatically restored keyword fired on the next edit")
	}

	m = planNudgeModel(t)
	m.editInput(tea.KeyPressMsg(tea.Key{Code: 'v', Text: "design the api", Mod: tea.ModCtrl}))
	if m.planModeHint.shown != 0 {
		t.Fatal("paste chord showed plan nudge")
	}

	m = planNudgeModel(t)
	m.setInput("plans")
	m.editInput(tea.KeyPressMsg(tea.Key{Code: tea.KeyBackspace}))
	if m.planModeHint.shown != 1 {
		t.Fatal("typed deletion crossing into a whole keyword did not show the nudge")
	}
}

func TestPlanNudgeExpiryAndSessionCap(t *testing.T) {
	m := planNudgeModel(t)
	for iteration := 0; iteration < 4; iteration++ {
		m.setInput("")
		for _, char := range "plan" {
			m.editInput(tea.KeyPressMsg(tea.Key{Code: char, Text: string(char)}))
		}
	}
	if m.planModeHint.shown != 3 {
		t.Fatalf("shown=%d", m.planModeHint.shown)
	}

	m = planNudgeModel(t)
	for _, char := range "plan" {
		m.editInput(tea.KeyPressMsg(tea.Key{Code: char, Text: string(char)}))
	}
	event := planNudgeClearEvent{nonce: m.planModeHint.nonce}
	updated, command := m.Update(event)
	m = updated.(*model)
	if command != nil || m.status != "ready" {
		t.Fatalf("command=%v status=%q", command != nil, m.status)
	}

	m.status = "Planning? Check out plan mode via shift+tab"
	m.planModeHint.nonce++
	updated, _ = m.Update(event)
	m = updated.(*model)
	if !strings.Contains(m.status, "Planning?") {
		t.Fatalf("stale expiry changed status: %q", m.status)
	}
}

func planNudgeModel(t *testing.T) *model {
	t.Helper()
	root := t.TempDir()
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry(ws, tools.PromptApprover{Mode: tools.PermissionAuto})
	t.Cleanup(func() { _ = registry.Close() })
	if err := registry.ConfigurePlanMode(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	return &model{
		runner:       &agent.Runner{Tools: registry},
		planModeHint: contextualHintState{enabled: true},
		status:       "ready",
	}
}
