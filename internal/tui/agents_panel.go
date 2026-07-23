package tui

import (
	"fmt"
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/lookcorner/go-cli/internal/agents"
	"github.com/lookcorner/go-cli/internal/personas"
)

type agentConfigTab uint8

const (
	agentConfigAgents agentConfigTab = iota
	agentConfigPersonas
)

type agentConfigState struct {
	tab       agentConfigTab
	selected  int
	query     []rune
	searching bool
	expanded  map[string]bool
	personas  []personas.Persona
	form      *personaForm
	confirm   *personas.Persona
	err       string
}

type personaForm struct {
	field                           int
	name, description, instructions []rune
	scope                           personas.Scope
}

func (m *model) openAgentConfig(tab agentConfigTab) {
	m.agentConfig = &agentConfigState{tab: tab, expanded: make(map[string]bool)}
	m.scroll = 0
	m.refreshAgentConfig()
	m.status = "agent configuration"
}

func (m *model) refreshAgentConfig() {
	state := m.agentConfig
	if state == nil {
		return
	}
	state.err = ""
	if m.runner == nil || m.runner.Personas == nil {
		state.personas = nil
		if state.tab == agentConfigPersonas {
			state.err = "Personas are unavailable"
		}
	} else {
		items, err := m.runner.Personas.List()
		if err != nil {
			state.err = err.Error()
		} else {
			state.personas = items
		}
	}
	state.selected = min(state.selected, max(m.agentConfigRowCount()-1, 0))
}

func (m *model) agentConfigDefinitions() []agents.Definition {
	if m.agentConfig == nil || m.runner == nil || m.runner.AgentDefinitions == nil {
		return nil
	}
	query := strings.TrimSpace(string(m.agentConfig.query))
	var result []agents.Definition
	for _, definition := range m.runner.AgentDefinitions() {
		search := strings.Join([]string{definition.Name, definition.Description, definition.Scope, definition.Model, strings.Join(definition.Tools, " "), strings.Join(definition.Skills, " ")}, " ")
		if fuzzyMatch(search, query) {
			result = append(result, definition)
		}
	}
	return result
}

func (m *model) agentConfigPersonas() []personas.Persona {
	if m.agentConfig == nil {
		return nil
	}
	query := strings.TrimSpace(string(m.agentConfig.query))
	var result []personas.Persona
	for _, persona := range m.agentConfig.personas {
		if fuzzyMatch(persona.Name+" "+persona.Description+" "+string(persona.Scope), query) {
			result = append(result, persona)
		}
	}
	return result
}

func (m *model) agentConfigRowCount() int {
	if m.agentConfig == nil {
		return 0
	}
	if m.agentConfig.tab == agentConfigPersonas {
		return len(m.agentConfigPersonas())
	}
	return len(m.agentConfigDefinitions())
}

func (m *model) agentConfigContent() string {
	state := m.agentConfig
	if state == nil {
		return ""
	}
	if state.form != nil {
		return m.personaFormContent()
	}
	var out strings.Builder
	out.WriteString("# Agent configuration\n\n")
	if state.tab == agentConfigAgents {
		out.WriteString("[Agents] Personas\n\n")
	} else {
		out.WriteString("Agents [Personas]\n\n")
	}
	if state.searching || len(state.query) > 0 {
		out.WriteString("Search: " + string(state.query) + "\n\n")
	}
	if state.tab == agentConfigAgents {
		definitions := m.agentConfigDefinitions()
		if len(definitions) == 0 {
			out.WriteString("No matching agents.\n")
		}
		for index, definition := range definitions {
			cursor := "  "
			if index == state.selected {
				cursor = "> "
			}
			out.WriteString(fmt.Sprintf("%s%s [%s]\n    %s\n", cursor, definition.Name, definition.Scope, definition.Description))
			if state.expanded[definition.Name] {
				if definition.Model != "" {
					out.WriteString("    Model: " + definition.Model + "\n")
				}
				if len(definition.Tools) > 0 {
					out.WriteString("    Tools: " + strings.Join(definition.Tools, ", ") + "\n")
				}
				if len(definition.Skills) > 0 {
					out.WriteString("    Skills: " + strings.Join(definition.Skills, ", ") + "\n")
				}
				if definition.Path != "" {
					out.WriteString("    Source: " + definition.Path + "\n")
				}
			}
		}
	} else {
		items := m.agentConfigPersonas()
		if len(items) == 0 {
			out.WriteString("No matching personas.\n")
		}
		for index, persona := range items {
			cursor := "  "
			if index == state.selected {
				cursor = "> "
			}
			capabilities := ""
			if persona.HasInputs {
				capabilities += " | inputs"
			}
			if persona.HasOutputs {
				capabilities += " | outputs"
			}
			out.WriteString(fmt.Sprintf("%s%s [%s%s]\n    %s\n", cursor, persona.Name, persona.Scope, capabilities, persona.Description))
		}
	}
	if state.err != "" {
		out.WriteString("\nError: " + state.err + "\n")
	}
	if state.confirm != nil {
		out.WriteString("\nDelete persona " + state.confirm.Name + "? Press Y to confirm or N to cancel.\n")
	}
	return strings.TrimSpace(out.String())
}

func (m *model) agentConfigHint() string {
	state := m.agentConfig
	if state == nil {
		return ""
	}
	if state.form != nil {
		return "Tab field | Enter create | Esc cancel"
	}
	if state.searching {
		return "Type to search | Enter accept | Esc clear"
	}
	if state.confirm != nil {
		return "Y confirm delete | N/Esc cancel"
	}
	if state.tab == agentConfigPersonas {
		return "Enter view | N new | D delete | Left/Right tabs | / search | R refresh | Esc close"
	}
	return "Enter view | E expand | Left/Right tabs | / search | R refresh | Esc close"
}

func (m *model) handleAgentConfigKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	state := m.agentConfig
	if state == nil {
		return m, nil
	}
	if state.form != nil {
		return m.handlePersonaFormKey(msg)
	}
	stroke, text := msg.Keystroke(), strings.ToLower(msg.Key().Text)
	if state.searching {
		switch stroke {
		case "enter":
			state.searching = false
		case "esc":
			state.searching, state.query = false, nil
		case "backspace":
			if len(state.query) > 0 {
				state.query = state.query[:len(state.query)-1]
			}
		default:
			if value := msg.Key().Text; value != "" {
				state.query = append(state.query, []rune(value)...)
			}
		}
		state.selected = 0
		return m, nil
	}
	if state.confirm != nil {
		if text == "y" {
			name, err := state.confirm.Name, m.runner.Personas.Delete(state.confirm.Path)
			state.confirm = nil
			if err != nil {
				state.err = err.Error()
			} else {
				m.refreshAgentConfig()
				m.status = "deleted persona " + name
			}
		} else if text == "n" || stroke == "esc" {
			state.confirm = nil
		}
		return m, nil
	}
	rows := m.agentConfigRowCount()
	switch {
	case stroke == "esc" || text == "q":
		m.agentConfig = nil
		m.status = "agent configuration closed"
	case stroke == "left" || stroke == "shift+tab":
		state.tab = agentConfigTab((int(state.tab) + 1) % 2)
		state.selected, state.query = 0, nil
		m.refreshAgentConfig()
	case stroke == "right" || stroke == "tab":
		state.tab = agentConfigTab((int(state.tab) + 1) % 2)
		state.selected, state.query = 0, nil
		m.refreshAgentConfig()
	case stroke == "up" || text == "k":
		state.selected = max(0, state.selected-1)
	case stroke == "down" || text == "j":
		state.selected = min(max(rows-1, 0), state.selected+1)
	case text == "/":
		state.searching = true
	case text == "r":
		m.refreshAgentConfig()
		m.status = "agent configuration refreshed"
	case rows > 0 && state.tab == agentConfigAgents && text == "e":
		definition := m.agentConfigDefinitions()[state.selected]
		state.expanded[definition.Name] = !state.expanded[definition.Name]
	case rows > 0 && state.tab == agentConfigAgents && (stroke == "enter" || text == "o"):
		m.viewAgentDefinition(m.agentConfigDefinitions()[state.selected])
	case state.tab == agentConfigPersonas && text == "n":
		if m.runner == nil || m.runner.Personas == nil {
			state.err = "Personas are unavailable"
		} else {
			state.form = &personaForm{scope: personas.ScopeUser}
			state.err = ""
		}
	case rows > 0 && state.tab == agentConfigPersonas && (stroke == "enter" || text == "o"):
		m.viewPersona(m.agentConfigPersonas()[state.selected])
	case rows > 0 && state.tab == agentConfigPersonas && text == "d":
		persona := m.agentConfigPersonas()[state.selected]
		if !persona.Editable() {
			state.err = "Cannot delete bundled personas"
		} else {
			state.confirm = &persona
		}
	}
	return m, nil
}

func (m *model) viewAgentDefinition(definition agents.Definition) {
	content := definition.Prompt
	if definition.Path != "" {
		if data, err := os.ReadFile(definition.Path); err == nil {
			content = string(data)
		}
	}
	if strings.TrimSpace(content) == "" {
		content = definition.Description
	}
	m.agentConfig = nil
	m.viewer = &readOnlyViewer{title: "Agent: " + definition.Name, content: content}
	m.status = "agent definition"
	m.scroll = 0
}

func (m *model) viewPersona(persona personas.Persona) {
	content, err := m.runner.Personas.Read(persona.Path)
	if err != nil {
		m.agentConfig.err = err.Error()
		return
	}
	m.agentConfig = nil
	m.viewer = &readOnlyViewer{title: "Persona: " + persona.Name, content: content}
	m.status = "persona definition"
	m.scroll = 0
}

func (m *model) personaFormContent() string {
	form := m.agentConfig.form
	fields := []struct{ label, value string }{
		{"Name", string(form.name)}, {"Description", string(form.description)},
		{"Instructions", string(form.instructions)}, {"Scope", string(form.scope)},
	}
	var out strings.Builder
	out.WriteString("# Create persona\n\n")
	for index, field := range fields {
		cursor := "  "
		if index == form.field {
			cursor = "> "
		}
		out.WriteString(cursor + field.label + ": " + field.value + "\n")
	}
	if m.agentConfig.err != "" {
		out.WriteString("\nError: " + m.agentConfig.err + "\n")
	}
	return strings.TrimSpace(out.String())
}

func (m *model) handlePersonaFormKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	state, form := m.agentConfig, m.agentConfig.form
	stroke := msg.Keystroke()
	if form.field == 3 && (stroke == "left" || stroke == "right" || stroke == "space") {
		if form.scope == personas.ScopeUser {
			form.scope = personas.ScopeProject
		} else {
			form.scope = personas.ScopeUser
		}
		return m, nil
	}
	switch stroke {
	case "esc":
		state.form, state.err = nil, ""
	case "tab", "down":
		form.field = (form.field + 1) % 4
	case "shift+tab", "up":
		form.field = (form.field + 3) % 4
	case "backspace":
		if form.field < 3 {
			value := personaFormField(form)
			if len(*value) > 0 {
				*value = (*value)[:len(*value)-1]
			}
		}
	case "enter":
		created, err := m.runner.Personas.Create(personas.Draft{
			Name: string(form.name), Description: string(form.description), Instructions: string(form.instructions), Scope: form.scope,
		})
		if err != nil {
			state.err = err.Error()
			return m, nil
		}
		state.form = nil
		m.refreshAgentConfig()
		m.status = "created persona " + created.Name
	default:
		if form.field < 3 && msg.Key().Text != "" {
			value := personaFormField(form)
			*value = append(*value, []rune(msg.Key().Text)...)
		}
	}
	return m, nil
}

func personaFormField(form *personaForm) *[]rune {
	switch form.field {
	case 0:
		return &form.name
	case 1:
		return &form.description
	default:
		return &form.instructions
	}
}
