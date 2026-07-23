package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/agents"
	"github.com/lookcorner/go-cli/internal/personas"
)

func TestAgentConfigCommandsOpenExpectedTabs(t *testing.T) {
	t.Setenv("GROK_HOME", t.TempDir())
	runner := &agent.Runner{
		AgentDefinitions: func() []agents.Definition {
			return []agents.Definition{{Name: "general-purpose", Description: "General work", Scope: "built-in"}}
		},
		Personas: personas.New(t.TempDir()),
	}
	for command, want := range map[string]agentConfigTab{"/config-agents": agentConfigAgents, "/agents": agentConfigAgents, "/personas": agentConfigPersonas} {
		m := &model{width: 80, height: 20, runner: runner}
		m.setInput(command)
		updated, cmd := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
		m = updated.(*model)
		if cmd != nil || m.agentConfig == nil || m.agentConfig.tab != want {
			t.Fatalf("command=%s state=%#v async=%v", command, m.agentConfig, cmd != nil)
		}
	}
}

func TestPersonaCreateUnavailableDoesNotOpenForm(t *testing.T) {
	m := &model{runner: &agent.Runner{}}
	m.openAgentConfig(agentConfigPersonas)
	updated, _ := m.handleAgentConfigKey(tea.KeyPressMsg(tea.Key{Code: 'n', Text: "n"}))
	m = updated.(*model)
	if m.agentConfig.form != nil || m.agentConfig.err != "Personas are unavailable" {
		t.Fatalf("state=%#v", m.agentConfig)
	}
}

func TestAgentConfigSearchExpandAndView(t *testing.T) {
	runner := &agent.Runner{AgentDefinitions: func() []agents.Definition {
		return []agents.Definition{
			{Name: "general-purpose", Description: "General work", Scope: "built-in", Model: "grok", Tools: []string{"read_file"}, Prompt: "Do the work."},
			{Name: "explore", Description: "Explore code", Scope: "built-in"},
		}
	}}
	m := &model{runner: runner}
	m.openAgentConfig(agentConfigAgents)
	updated, _ := m.handleAgentConfigKey(tea.KeyPressMsg(tea.Key{Code: '/', Text: "/"}))
	m = updated.(*model)
	for _, character := range "gp" {
		updated, _ = m.handleAgentConfigKey(tea.KeyPressMsg(tea.Key{Code: character, Text: string(character)}))
		m = updated.(*model)
	}
	if rows := m.agentConfigDefinitions(); len(rows) != 1 || rows[0].Name != "general-purpose" {
		t.Fatalf("filtered agents=%#v", rows)
	}
	updated, _ = m.handleAgentConfigKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	updated, _ = m.handleAgentConfigKey(tea.KeyPressMsg(tea.Key{Code: 'e', Text: "e"}))
	m = updated.(*model)
	if !m.agentConfig.expanded["general-purpose"] || !strings.Contains(m.agentConfigContent(), "Model: grok") {
		t.Fatalf("agent was not expanded:\n%s", m.agentConfigContent())
	}
	updated, _ = m.handleAgentConfigKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if m.agentConfig != nil || m.viewer == nil || m.viewer.content != "Do the work." {
		t.Fatalf("agent viewer=%#v state=%#v", m.viewer, m.agentConfig)
	}
}

func TestPersonaCreateDeleteAndBundledGuard(t *testing.T) {
	home, workspace := t.TempDir(), t.TempDir()
	t.Setenv("GROK_HOME", home)
	service := personas.New(workspace)
	bundledPath := filepath.Join(home, "bundled", "personas", "bundled.toml")
	if err := os.MkdirAll(filepath.Dir(bundledPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bundledPath, []byte("description = \"Read only\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	local, err := service.Create(personas.Draft{Name: "local", Description: "Local", Scope: personas.ScopeProject})
	if err != nil {
		t.Fatal(err)
	}
	m := &model{runner: &agent.Runner{Personas: service}}
	m.openAgentConfig(agentConfigPersonas)
	if len(m.agentConfigPersonas()) != 2 {
		t.Fatalf("personas=%#v", m.agentConfig.personas)
	}
	updated, cmd := m.handleAgentConfigKey(tea.KeyPressMsg(tea.Key{Code: 'd', Text: "d"}))
	m = updated.(*model)
	if cmd != nil || m.agentConfig.confirm != nil || m.agentConfig.err != "Cannot delete bundled personas" {
		t.Fatalf("bundled delete state=%#v", m.agentConfig)
	}
	m.agentConfig.err, m.agentConfig.selected = "", 1
	updated, _ = m.handleAgentConfigKey(tea.KeyPressMsg(tea.Key{Code: 'd', Text: "d"}))
	m = updated.(*model)
	if m.agentConfig.confirm == nil {
		t.Fatal("local delete did not ask for confirmation")
	}
	updated, _ = m.handleAgentConfigKey(tea.KeyPressMsg(tea.Key{Code: 'y', Text: "y"}))
	m = updated.(*model)
	if _, err := os.Stat(local.Path); !os.IsNotExist(err) || len(m.agentConfigPersonas()) != 1 {
		t.Fatalf("local path err=%v personas=%#v", err, m.agentConfig.personas)
	}

	updated, _ = m.handleAgentConfigKey(tea.KeyPressMsg(tea.Key{Code: 'n', Text: "n"}))
	m = updated.(*model)
	m.agentConfig.form.name = []rune("reviewer")
	m.agentConfig.form.field = 1
	for _, key := range []tea.Key{{Code: 'R', Text: "R"}, {Code: tea.KeySpace, Text: " "}, {Code: 'c', Text: "c"}} {
		updated, _ = m.handleAgentConfigKey(tea.KeyPressMsg(key))
		m = updated.(*model)
	}
	if string(m.agentConfig.form.description) != "R c" {
		t.Fatalf("description=%q", m.agentConfig.form.description)
	}
	m.agentConfig.form.description = []rune("Reviews code")
	m.agentConfig.form.scope = personas.ScopeProject
	updated, _ = m.handleAgentConfigKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if m.agentConfig.form != nil || m.agentConfig.err != "" || len(m.agentConfigPersonas()) != 2 {
		t.Fatalf("create state=%#v personas=%#v", m.agentConfig.form, m.agentConfig.personas)
	}
	if _, err := os.Stat(filepath.Join(workspace, ".grok", "personas", "reviewer.toml")); err != nil {
		t.Fatal(err)
	}
}
