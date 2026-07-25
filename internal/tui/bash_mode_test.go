package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/tools"
	"github.com/lookcorner/go-cli/internal/workspace"
)

func TestBangOnEmptyPromptEntersBashMode(t *testing.T) {
	m := &model{ctx: context.Background(), runner: &agent.Runner{}, width: 60, height: 16, status: "ready"}
	updated, command := m.handleKey(tea.KeyPressMsg(tea.Key{Code: '!', Text: "!"}))
	m = updated.(*model)
	if command != nil || !m.bashInput || len(m.input) != 0 || m.status != "Run shell command" {
		t.Fatalf("command=%v bash=%v input=%q status=%q", command != nil, m.bashInput, m.input, m.status)
	}
	view := stripUIANSI(m.View().Content)
	if !strings.Contains(view, "! █") || !strings.Contains(view, "Run shell command") || !strings.Contains(view, "Enter run") {
		t.Fatalf("bash composer chrome missing: %q", view)
	}
}

func TestBashModeEnterRunsCommandInMultilineMode(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry(ws, tools.PromptApprover{Mode: tools.PermissionAuto})
	defer registry.Close()
	m := &model{
		ctx: context.Background(), runner: &agent.Runner{Tools: registry},
		bashInput: true, multiline: true,
	}
	m.setInput("printf done")
	m.bashInput = true

	updated, command := m.handleKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command == nil || !m.running || m.bashInput || string(m.input) != "" {
		t.Fatalf("command=%v running=%v bash=%v input=%q", command != nil, m.running, m.bashInput, m.input)
	}
	updated, _ = m.Update(command())
	m = updated.(*model)
	if m.running || m.status != "shell completed" || !strings.Contains(m.transcript.String(), "done") {
		t.Fatalf("running=%v status=%q transcript=%q", m.running, m.status, m.transcript.String())
	}
}

func TestEmptyBashModeExitKeysReturnToNormal(t *testing.T) {
	keys := []tea.Key{
		{Code: tea.KeyEsc},
		{Code: tea.KeyBackspace},
		{Code: 'c', Text: "c", Mod: tea.ModCtrl},
		{Code: 'u', Text: "u", Mod: tea.ModCtrl},
		{Code: 'w', Text: "w", Mod: tea.ModCtrl},
	}
	for _, key := range keys {
		m := &model{ctx: context.Background(), runner: &agent.Runner{}, bashInput: true, status: "Run shell command"}
		updated, command := m.handleKey(tea.KeyPressMsg(key))
		m = updated.(*model)
		if command != nil || m.bashInput || m.status != "ready" {
			t.Fatalf("key=%q command=%v bash=%v status=%q", key.String(), command != nil, m.bashInput, m.status)
		}
	}
}

func TestHistoryRestoresBashModeWithoutLiteralPrefix(t *testing.T) {
	m := &model{history: []string{"! ls -la"}, historyIndex: -1}
	m.browseHistory(-1)
	if !m.bashInput || string(m.input) != "ls -la" || m.cursor != len([]rune("ls -la")) {
		t.Fatalf("bash=%v input=%q cursor=%d", m.bashInput, m.input, m.cursor)
	}
	m.closeHistory()
	if m.bashInput || len(m.input) != 0 {
		t.Fatalf("closed history bash=%v input=%q", m.bashInput, m.input)
	}
}

func TestPastedBangCommandEntersBashMode(t *testing.T) {
	m := &model{}
	updated, command := m.handlePaste("! printf pasted")
	m = updated.(*model)
	if command != nil || !m.bashInput || string(m.input) != "printf pasted" {
		t.Fatalf("command=%v bash=%v input=%q", command != nil, m.bashInput, m.input)
	}
}

func TestMinimalBashModeShowsShellChrome(t *testing.T) {
	m := &model{minimal: true, bashInput: true, width: 60, height: 16, status: "ready"}
	view := stripUIANSI(m.View().Content)
	if !strings.Contains(view, "! █") || !strings.Contains(view, "Run shell command") {
		t.Fatalf("minimal bash chrome missing: %q", view)
	}
}
