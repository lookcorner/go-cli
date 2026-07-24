package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/skills"
)

func TestSlashMenuFuzzyCompletionAndExecution(t *testing.T) {
	m := &model{width: 70, height: 18, modelName: "test"}
	m.setInput("/m")
	modelMatches := 0
	for _, item := range m.slashSuggestions() {
		if item.description == "Switch model" {
			modelMatches++
			if item.insert != "/m " {
				t.Fatalf("exact alias did not win: %#v", item)
			}
		}
	}
	if modelMatches != 1 {
		t.Fatalf("model matches=%d", modelMatches)
	}

	m.setInput("/mo")
	items := m.slashSuggestions()
	if len(items) == 0 || items[0].insert != "/model " {
		t.Fatalf("items=%#v", items)
	}
	updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab}))
	m = updated.(*model)
	if command != nil || string(m.input) != "/model " {
		t.Fatalf("command=%v input=%q", command != nil, m.input)
	}

	m.setInput("/sett")
	updated, command = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command != nil || m.settings == nil || m.status != "settings" {
		t.Fatalf("command=%v settings=%v status=%q", command != nil, m.settings != nil, m.status)
	}
}

func TestSlashMenuNavigationDismissAndQueryReset(t *testing.T) {
	m := &model{width: 70, height: 18, modelName: "test"}
	m.setInput("/")
	items := m.slashSuggestions()
	if len(items) < 2 {
		t.Fatalf("items=%#v", items)
	}
	updated, _ := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyUp}))
	m = updated.(*model)
	if m.slashSelected != len(items)-1 {
		t.Fatalf("selected=%d items=%d", m.slashSelected, len(items))
	}
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEsc}))
	m = updated.(*model)
	if len(m.slashSuggestions()) != 0 || m.slashDismissed != "/" {
		t.Fatalf("dismissed=%q items=%#v", m.slashDismissed, m.slashSuggestions())
	}
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: 'm', Text: "m"}))
	m = updated.(*model)
	items = m.slashSuggestions()
	if m.slashSelected != 0 || len(items) == 0 {
		t.Fatalf("selected=%d items=%#v", m.slashSelected, items)
	}
}

func TestSlashMenuCompletesModelAndReasoningEffort(t *testing.T) {
	m := &model{
		width: 70, height: 18, modelName: "test",
		runner: &agent.Runner{ModelOptions: []agent.ModelOption{
			{
				ID: "grok-fast", Name: "Grok Fast", SupportsReasoningEffort: true,
				ReasoningEfforts: []agent.ReasoningEffortOption{
					{ID: "high", Label: "High"},
					{ID: "low", Label: "Low"},
				},
			},
			{ID: "plain", Name: "Plain"},
		}},
	}
	m.setInput("/model gr")
	updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab}))
	m = updated.(*model)
	if command != nil || string(m.input) != "/model grok-fast " {
		t.Fatalf("command=%v input=%q", command != nil, m.input)
	}
	items := m.slashSuggestions()
	if len(items) != 2 || items[0].label != "high" || items[1].label != "low" {
		t.Fatalf("efforts=%#v", items)
	}
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: 'h', Text: "h"}))
	m = updated.(*model)
	items = m.slashSuggestions()
	if len(items) != 1 || items[0].label != "high" {
		t.Fatalf("filtered efforts=%#v", items)
	}
	updated, command = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab}))
	m = updated.(*model)
	if command != nil || string(m.input) != "/model grok-fast high" {
		t.Fatalf("command=%v input=%q", command != nil, m.input)
	}
}

func TestSlashMenuCompletesEffortAfterSpacedModelName(t *testing.T) {
	m := &model{
		width: 70, height: 18, modelName: "test",
		runner: &agent.Runner{ModelOptions: []agent.ModelOption{{
			ID: "grok-fast", Name: "Grok Fast", SupportsReasoningEffort: true,
			ReasoningEfforts: []agent.ReasoningEffortOption{
				{ID: "high", Label: "High"},
				{ID: "low", Label: "Low"},
			},
		}}},
	}
	m.setInput("/model Grok Fast h")
	items := m.slashSuggestions()
	if len(items) != 1 || items[0].label != "high" {
		t.Fatalf("efforts=%#v", items)
	}
	updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab}))
	m = updated.(*model)
	if command != nil || string(m.input) != "/model Grok Fast high" {
		t.Fatalf("command=%v input=%q", command != nil, m.input)
	}
}

func TestSlashMenuArgumentCompletionCanExecute(t *testing.T) {
	m := &model{width: 70, height: 18, modelName: "test", themeName: "groknight", theme: paletteFor("groknight")}
	m.setInput("/theme tok")
	updated, command := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(*model)
	if command != nil || m.themeName != "tokyonight" || m.status != "theme: tokyonight" {
		t.Fatalf("command=%v theme=%q status=%q", command != nil, m.themeName, m.status)
	}
}

func TestSlashMenuRespectsCapabilitiesAndScreenMode(t *testing.T) {
	m := &model{width: 70, height: 18, modelName: "test"}
	m.setInput("/")
	labels := slashLabels(m.slashSuggestions())
	for _, hidden := range []string{"/auto", "/feedback", "/fullscreen", "/imagine", "/loop", "/share"} {
		if strings.Contains(labels, hidden+"\n") {
			t.Fatalf("unexpected %s in %q", hidden, labels)
		}
	}
	if !strings.Contains(labels, "/minimal\n") {
		t.Fatalf("minimal missing from %q", labels)
	}

	m.minimal = true
	m.slashQuery = ""
	labels = slashLabels(m.slashSuggestions())
	if strings.Contains(labels, "/minimal\n") || !strings.Contains(labels, "/fullscreen\n") {
		t.Fatalf("screen commands=%q", labels)
	}
}

func TestSlashMenuRendersDropdownAndSelectedGhost(t *testing.T) {
	m := &model{width: 70, height: 18, modelName: "test", workspace: "/workspace", status: "ready"}
	m.setInput("/sett")
	content := stripUIANSI(m.View().Content)
	if !strings.Contains(content, "› /settings") || !strings.Contains(content, "> /sett█ings") {
		t.Fatalf("content=%q", content)
	}
}

func TestSlashMenuIncludesInvocableSkillsAndQualifiesConflicts(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"deploy", "model"} {
		dir := filepath.Join(root, ".grok", "skills", name)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		data := "---\nname: " + name + "\ndescription: Run " + name + "\nuser-invocable: true\nargument-hint: '[target]'\n---\nInstructions\n"
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	catalog, err := skills.Discover(root, skills.Config{})
	if err != nil {
		t.Fatal(err)
	}
	m := &model{runner: &agent.Runner{Skills: catalog}, width: 70, height: 18}

	m.setInput("/dep")
	items := m.slashSuggestions()
	if len(items) == 0 || items[0].insert != "/deploy " || !items[0].chain {
		t.Fatalf("deploy=%#v", items)
	}
	m.setInput("/local:mod")
	items = m.slashSuggestions()
	if len(items) == 0 || items[0].insert != "/local:model " {
		t.Fatalf("qualified model skill=%#v", items)
	}
}

func slashLabels(items []slashSuggestion) string {
	var out strings.Builder
	for _, item := range items {
		out.WriteString(strings.Fields(item.label)[0])
		out.WriteByte('\n')
	}
	return out.String()
}
