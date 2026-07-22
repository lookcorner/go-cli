package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestScrollSearchMatchesNavigatesAndHighlights(t *testing.T) {
	search := newScrollSearch()
	search.query = []rune(`(?i)alpha`)
	search.cursor = len(search.query)
	lines := []string{"Alpha beta", "beta alpha"}
	search.update(lines)
	if len(search.matches) != 2 || search.current != 0 || search.err != nil {
		t.Fatalf("search=%#v", search)
	}
	highlighted := search.highlighted(lines)
	if !strings.Contains(highlighted[0], ansiSearchMatch+"Alpha") || !strings.Contains(highlighted[1], ansiSearchOther+"alpha") {
		t.Fatalf("highlights=%q", highlighted)
	}
	if got := strings.Join([]string{stripUIANSI(highlighted[0]), stripUIANSI(highlighted[1])}, "\n"); got != strings.Join(lines, "\n") {
		t.Fatalf("highlight changed text: %q", got)
	}
	search.step(-1)
	if search.current != 1 {
		t.Fatalf("previous did not wrap: %d", search.current)
	}
	search.step(1)
	if search.current != 0 {
		t.Fatalf("next did not wrap: %d", search.current)
	}
}

func TestScrollSearchRejectsInvalidRegex(t *testing.T) {
	search := newScrollSearch()
	search.query = []rune("[")
	search.update([]string{"text"})
	if search.err == nil || len(search.matches) != 0 || search.current != -1 || search.status() != "search: invalid pattern" {
		t.Fatalf("invalid search=%#v status=%q", search, search.status())
	}
}

func TestTUIScrollbackSearchWorkflow(t *testing.T) {
	m := &model{width: 40, height: 12, status: "ready"}
	for line := 0; line < 30; line++ {
		text := "ordinary"
		if line == 3 || line == 25 {
			text = "needle"
		}
		fmt.Fprintf(&m.transcript, "line %02d %s\n", line, text)
	}
	press := func(key tea.Key) {
		updated, _ := m.Update(tea.KeyPressMsg(key))
		m = updated.(*model)
	}

	press(tea.Key{Code: tea.KeyTab})
	press(tea.Key{Code: '/', Text: "/"})
	press(tea.Key{Code: 'n', Text: "needle"})
	if m.scrollSearch == nil || len(m.scrollSearch.matches) != 2 || m.scrollSearch.current != 0 || m.scroll == 0 {
		t.Fatalf("opened search=%#v scroll=%d", m.scrollSearch, m.scroll)
	}
	view := m.View().Content
	if !strings.Contains(view, "Search scrollback") || !strings.Contains(view, ansiSearchMatch+"needle") || !strings.Contains(view, "1/2") {
		t.Fatalf("search view=%q", view)
	}
	firstScroll := m.scroll

	press(tea.Key{Code: tea.KeyEnter})
	press(tea.Key{Code: 'n', Text: "n"})
	if m.scrollSearch.current != 1 || m.scroll >= firstScroll {
		t.Fatalf("next current=%d scroll=%d", m.scrollSearch.current, m.scroll)
	}
	press(tea.Key{Code: 'N', Text: "N", Mod: tea.ModShift})
	if m.scrollSearch.current != 0 {
		t.Fatalf("previous current=%d", m.scrollSearch.current)
	}
	press(tea.Key{Code: tea.KeyEsc})
	if m.scrollSearch != nil || !m.scrollFocused {
		t.Fatalf("closed search=%#v focus=%v", m.scrollSearch, m.scrollFocused)
	}
}

func TestFindCommandAndStreamingRefresh(t *testing.T) {
	m := &model{width: 50, height: 14, status: "ready", input: []rune("/find beta"), cursor: len([]rune("/find beta"))}
	m.transcript.WriteString("alpha beta\n")
	updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command != nil || m.scrollSearch == nil || m.scrollSearch.queryText() != "beta" || len(m.scrollSearch.matches) != 1 || !m.scrollFocused {
		t.Fatalf("find command=%v model=%#v", command != nil, m.scrollSearch)
	}
	updated, _ = m.Update(textEvent{text: "beta again\n"})
	m = updated.(*model)
	if len(m.scrollSearch.matches) != 2 || m.scroll != m.maxTranscriptScroll() {
		t.Fatalf("streaming matches=%#v scroll=%d", m.scrollSearch.matches, m.scroll)
	}

	empty := &model{status: "ready", scrollFocused: true}
	updated, _ = empty.Update(tea.KeyPressMsg(tea.Key{Code: '/', Text: "/"}))
	empty = updated.(*model)
	if empty.scrollSearch != nil || empty.status != "scrollback is empty" {
		t.Fatalf("empty search=%#v status=%q", empty.scrollSearch, empty.status)
	}
}

func TestScrollSearchEditingAndEscapePrecedence(t *testing.T) {
	m := &model{width: 40, height: 12, scrollFocused: true, selection: &textSelection{}}
	m.transcript.WriteString("acb\n")
	m.openScrollSearch("ab")
	press := func(key tea.Key) {
		updated, _ := m.Update(tea.KeyPressMsg(key))
		m = updated.(*model)
	}
	press(tea.Key{Code: tea.KeyLeft})
	press(tea.Key{Code: 'c', Text: "c"})
	if got := m.scrollSearch.queryText(); got != "acb" || len(m.scrollSearch.matches) != 1 {
		t.Fatalf("middle insert query=%q matches=%#v", got, m.scrollSearch.matches)
	}
	press(tea.Key{Code: tea.KeyBackspace})
	if got := m.scrollSearch.queryText(); got != "ab" || len(m.scrollSearch.matches) != 0 {
		t.Fatalf("backspace query=%q matches=%#v", got, m.scrollSearch.matches)
	}
	press(tea.Key{Code: tea.KeyEsc})
	if m.scrollSearch != nil || m.selection == nil {
		t.Fatalf("escape search=%#v selection=%#v", m.scrollSearch, m.selection)
	}
}
