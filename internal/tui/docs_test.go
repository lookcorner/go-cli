package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/lookcorner/go-cli/internal/agent"
	guides "github.com/lookcorner/go-cli/internal/docs"
)

func TestDocsCommandsOpenListAndWebWithoutModelTurn(t *testing.T) {
	for _, prompt := range []string{"/docs", "/docs how-to", "/docs HOWTO", "/howto list", "/guides tui", "/docs guide"} {
		m := &model{ctx: context.Background(), runner: &agent.Runner{}, width: 80, height: 18}
		m.setInput(prompt)
		updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
		m = updated.(*model)
		if command != nil || m.docs == nil || m.docs.guide != nil || m.running || len(m.docs.guides) != 24 || m.status != "how-to guides" {
			t.Fatalf("prompt=%q command=%v docs=%#v running=%v status=%q", prompt, command != nil, m.docs, m.running, m.status)
		}
	}

	for _, target := range []string{"web", "ONLINE", "browser", "site", "www"} {
		var opened string
		m := &model{ctx: context.Background(), runner: &agent.Runner{OpenURL: func(url string) bool { opened = url; return true }}}
		m.setInput("/docs " + target)
		updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
		m = updated.(*model)
		if command != nil || opened != guides.BuildURL || m.running || m.status != "documentation opened" || m.transcript.Len() != 0 {
			t.Fatalf("target=%q command=%v opened=%q running=%v status=%q transcript=%q", target, command != nil, opened, m.running, m.status, m.transcript.String())
		}
	}
}

func TestDocsWebFallbackAndUnknownTarget(t *testing.T) {
	m := &model{ctx: context.Background(), runner: &agent.Runner{OpenURL: func(string) bool { return false }}, width: 80, height: 18}
	m.setInput("/docs web")
	updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command != nil || m.status != "documentation link" || !strings.Contains(m.transcript.String(), guides.BuildURL) {
		t.Fatalf("command=%v status=%q transcript=%q", command != nil, m.status, m.transcript.String())
	}

	m.setInput("/docs not-a-real-guide")
	updated, command = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command != nil || m.running || m.docs != nil || m.status != "docs target invalid" || !strings.Contains(m.transcript.String(), "Unknown docs target") || !strings.Contains(m.transcript.String(), "Getting Started") {
		t.Fatalf("command=%v running=%v docs=%v status=%q transcript=%q", command != nil, m.running, m.docs != nil, m.status, m.transcript.String())
	}
}

func TestDocsDirectTitleAndPickerNavigation(t *testing.T) {
	m := &model{ctx: context.Background(), runner: &agent.Runner{}, width: 64, height: 12}
	m.setInput("/docs getting STARTED")
	updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command != nil || m.docs == nil || m.docs.guide == nil || !m.docs.standalone || m.docs.guide.Title != "Getting Started" || !strings.Contains(stripUIANSI(m.View().Content), "Getting Started") {
		t.Fatalf("command=%v docs=%#v content=%q", command != nil, m.docs, stripUIANSI(m.View().Content))
	}
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEsc}))
	m = updated.(*model)
	if m.docs != nil || m.status != "ready" {
		t.Fatalf("direct guide did not close: docs=%#v status=%q", m.docs, m.status)
	}

	m.openDocs()
	if content := stripUIANSI(m.View().Content); !strings.Contains(content, "How-to Guides") || !strings.Contains(content, "Installation, first launch") || !strings.Contains(content, "Up/Down or j/k select") {
		t.Fatalf("picker content=%q", content)
	}
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown}))
	m = updated.(*model)
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if m.docs.guide == nil || m.docs.guide.Title != "Authentication" || m.docs.standalone {
		t.Fatalf("selected guide=%#v", m.docs)
	}
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEsc}))
	m = updated.(*model)
	if m.docs == nil || m.docs.guide != nil || m.docs.selected != 1 || m.status != "how-to guides" {
		t.Fatalf("picker was not restored: docs=%#v status=%q", m.docs, m.status)
	}
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEsc}))
	m = updated.(*model)
	if m.docs != nil || m.status != "ready" {
		t.Fatalf("picker did not close: docs=%#v status=%q", m.docs, m.status)
	}
}

func TestDocsViewerScrollIsBounded(t *testing.T) {
	item := guides.Guide{Title: "Long", Content: "# Long\n\n" + strings.Repeat("line\n\n", 40)}
	m := &model{width: 40, height: 10, docs: &docsState{guide: &item, standalone: true}}
	if hint := m.docsHint(); !strings.Contains(hint, "Esc close") {
		t.Fatalf("standalone hint=%q", hint)
	}
	updated, _ := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyHome}))
	m = updated.(*model)
	if m.scroll == 0 || m.scroll != m.maxDocsScroll() {
		t.Fatalf("home scroll=%d max=%d", m.scroll, m.maxDocsScroll())
	}
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown}))
	m = updated.(*model)
	if m.scroll >= m.maxDocsScroll() {
		t.Fatalf("down did not scroll toward end: scroll=%d max=%d", m.scroll, m.maxDocsScroll())
	}
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyUp}))
	m = updated.(*model)
	if m.scroll != m.maxDocsScroll() {
		t.Fatalf("up did not scroll toward start: scroll=%d max=%d", m.scroll, m.maxDocsScroll())
	}
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnd}))
	m = updated.(*model)
	if m.scroll != 0 {
		t.Fatalf("end scroll=%d", m.scroll)
	}
}

func TestDocsPickerKeysHelpersAndTimelineIsolation(t *testing.T) {
	m := &model{width: 70, height: 16, showTimeline: true, docs: &docsState{guides: guides.All(), selected: 1}}
	m.transcriptMessages = []transcriptMessage{{start: 0, role: "user"}, {start: 2, role: "user"}}
	if m.timelineWidth() != 0 {
		t.Fatal("docs panel exposed conversation timeline")
	}

	for _, key := range []tea.Key{{Code: 'k', Text: "k"}, {Code: tea.KeyUp}} {
		updated, command := m.Update(tea.KeyPressMsg(key))
		m = updated.(*model)
		if command != nil || m.docs.selected != 0 {
			t.Fatalf("key=%v selected=%d command=%v", key, m.docs.selected, command != nil)
		}
	}
	for _, key := range []tea.Key{{Code: 'j', Text: "j"}, {Code: tea.KeyDown}} {
		updated, command := m.Update(tea.KeyPressMsg(key))
		m = updated.(*model)
		if command != nil {
			t.Fatalf("key=%v command=%v", key, command != nil)
		}
	}
	updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: 'x', Text: "x"}))
	m = updated.(*model)
	if command != nil || m.docs.guide != nil {
		t.Fatalf("ignored key command=%v guide=%#v", command != nil, m.docs.guide)
	}
	_, quit := m.Update(tea.KeyPressMsg(tea.Key{Code: 'q', Text: "q", Mod: tea.ModCtrl}))
	if quit == nil {
		t.Fatal("Ctrl-Q did not request quit")
	}

	if got := (&model{}).docsContent(); got != "" {
		t.Fatalf("nil docs content=%q", got)
	}
	if got := (&model{}).maxDocsScroll(); got != 0 {
		t.Fatalf("nil docs max scroll=%d", got)
	}
	if got := escapeDocsText("**bad**\n[name]"); got != "\\*\\*bad\\*\\* \\[name\\]" {
		t.Fatalf("escaped=%q", got)
	}

	item := guides.Guide{Title: "One", Content: "# One"}
	m.docs = &docsState{guides: []guides.Guide{item}, guide: &item}
	if got := m.docsContent(); got != "# One" {
		t.Fatalf("guide content=%q", got)
	}
	if hint := m.docsHint(); !strings.Contains(hint, "Esc guides") {
		t.Fatalf("picker guide hint=%q", hint)
	}
}
