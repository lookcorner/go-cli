package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestBracketedPasteInsertsAtCursorAsOneUndoStep(t *testing.T) {
	m := &model{status: "ready"}
	m.setInput("before after")
	m.cursor = len([]rune("before "))

	updated, command := m.Update(tea.PasteMsg{Content: "世界\nline\rthird\r\n"})
	m = updated.(*model)
	if command != nil {
		t.Fatal("paste returned a command")
	}
	if got, want := string(m.input), "before 世界\nline\nthird\r\nafter"; got != want {
		t.Fatalf("input=%q want=%q", got, want)
	}
	if len(m.inputUndo) != 1 {
		t.Fatalf("undo entries=%d", len(m.inputUndo))
	}
	m.undoInput()
	if got := string(m.input); got != "before after" {
		t.Fatalf("undo input=%q", got)
	}
}

func TestBracketedPasteDoesNotTriggerPlanNudge(t *testing.T) {
	m := planNudgeModel(t)
	updated, command := m.Update(tea.PasteMsg{Content: "design the API"})
	m = updated.(*model)
	if command != nil || string(m.input) != "design the API" || m.planModeHint.shown != 0 {
		t.Fatalf("command=%v input=%q hint=%#v", command != nil, m.input, m.planModeHint)
	}
	updated, command = m.Update(tea.KeyPressMsg(tea.Key{Code: '!', Text: "!"}))
	m = updated.(*model)
	if command != nil || m.planModeHint.shown != 0 {
		t.Fatal("edit after pasted planning text triggered the nudge")
	}
}

func TestBracketedPasteRespectsInputContext(t *testing.T) {
	blocked := &model{settings: &settingsState{}, input: []rune("keep"), cursor: 4}
	updated, _ := blocked.Update(tea.PasteMsg{Content: " ignored"})
	blocked = updated.(*model)
	if got := string(blocked.input); got != "keep" {
		t.Fatalf("settings paste changed input=%q", got)
	}

	search := &model{
		historySearch: &historySearchState{},
		history:       []string{"alpha one", "beta two"},
		input:         []rune("alpha"),
		cursor:        len("alpha"),
	}
	search.refreshHistorySearch()
	updated, _ = search.Update(tea.PasteMsg{Content: " one"})
	search = updated.(*model)
	if got := string(search.input); got != "alpha one" {
		t.Fatalf("search input=%q", got)
	}
	if len(search.historySearch.results) != 1 || !strings.Contains(search.historySearch.results[0], "alpha one") {
		t.Fatalf("search results=%v", search.historySearch.results)
	}

	plan := &model{planReview: &planReviewState{editing: true}, input: []rune("revise "), cursor: 7}
	updated, _ = plan.Update(tea.PasteMsg{Content: "the API"})
	plan = updated.(*model)
	if got := string(plan.input); got != "revise the API" {
		t.Fatalf("plan feedback=%q", got)
	}
}

func TestBracketedPasteIgnoresEmptyContent(t *testing.T) {
	m := &model{input: []rune("unchanged"), cursor: 4}
	updated, command := m.Update(tea.PasteMsg{})
	m = updated.(*model)
	if command != nil || string(m.input) != "unchanged" || len(m.inputUndo) != 0 {
		t.Fatalf("command=%v input=%q undo=%d", command != nil, m.input, len(m.inputUndo))
	}
}
