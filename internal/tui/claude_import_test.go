package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/lookcorner/go-cli/internal/claudeimport"
)

func TestClaudeImportSelectionAndRedaction(t *testing.T) {
	item := claudeimport.Item{ID: "env", Scope: claudeimport.Global, Kind: claudeimport.Environment, Name: "TOKEN", Value: "top-secret"}
	m := &model{claudeImport: &claudeImportState{plan: claudeimport.Plan{Items: []claudeimport.Item{item}}, selected: map[string]bool{"env": true}}}
	content := m.claudeImportContent()
	if strings.Contains(content, "top-secret") || !strings.Contains(content, "<redacted>") {
		t.Fatalf("content=%q", content)
	}
	updated, _ := m.handleClaudeImportKey(tea.KeyPressMsg(tea.Key{Code: ' ', Text: " "}))
	m = updated.(*model)
	if m.claudeImport.selected["env"] {
		t.Fatal("space did not toggle selection")
	}
	updated, _ = m.handleClaudeImportKey(tea.KeyPressMsg(tea.Key{Code: 'a', Text: "a"}))
	m = updated.(*model)
	if !m.claudeImport.selected["env"] {
		t.Fatal("all did not select")
	}
	updated, _ = m.handleClaudeImportKey(tea.KeyPressMsg(tea.Key{Code: 'n', Text: "n"}))
	m = updated.(*model)
	if m.claudeImport.selected["env"] {
		t.Fatal("none did not clear selection")
	}
	updated, _ = m.handleClaudeImportKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEsc}))
	if updated.(*model).claudeImport != nil {
		t.Fatal("escape did not cancel")
	}
}
