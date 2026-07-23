package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/lookcorner/go-cli/internal/claudeimport"
)

func (m *model) openClaudeImport() {
	plan := claudeimport.Scan(m.workspace)
	selected := make(map[string]bool, len(plan.Items))
	for _, item := range plan.Items {
		selected[item.ID] = true
	}
	m.claudeImport = &claudeImportState{plan: plan, selected: selected}
	m.scroll = 0
	if len(plan.Items) == 0 {
		m.status = "No Claude settings found to import"
	} else {
		m.status = fmt.Sprintf("%d Claude setting(s) found", len(plan.Items))
	}
}

func (m *model) claudeImportContent() string {
	state := m.claudeImport
	if state == nil {
		return ""
	}
	var out strings.Builder
	out.WriteString("# Import Claude settings\n\n")
	if len(state.plan.Items) == 0 {
		out.WriteString("No Claude settings found to import. Press Enter to disable migrated Claude compatibility fallbacks.")
		return out.String()
	}
	lastScope := claudeimport.Scope("")
	for index, item := range state.plan.Items {
		if item.Scope != lastScope {
			out.WriteString("\n## " + strings.Title(string(item.Scope)) + "\n\n")
			lastScope = item.Scope
		}
		cursor := "  "
		if index == state.current {
			cursor = "> "
		}
		check := "[ ]"
		if state.selected[item.ID] {
			check = "[x]"
		}
		out.WriteString(fmt.Sprintf("%s%s %-11s %s\n", cursor, check, item.Kind, item.Label()))
	}
	if len(state.plan.Warnings) > 0 {
		out.WriteString(fmt.Sprintf("\n%d source warning(s) will be skipped.", len(state.plan.Warnings)))
	}
	return strings.TrimSpace(out.String())
}

func (m *model) handleClaudeImportKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	state := m.claudeImport
	if state == nil || state.busy {
		return m, nil
	}
	stroke := msg.Keystroke()
	switch strings.ToLower(msg.Key().Text) {
	case "j":
		stroke = "down"
	case "k":
		stroke = "up"
	case "a":
		for _, item := range state.plan.Items {
			state.selected[item.ID] = true
		}
		return m, nil
	case "n":
		for _, item := range state.plan.Items {
			state.selected[item.ID] = false
		}
		return m, nil
	}
	switch stroke {
	case "up":
		state.current = max(0, state.current-1)
	case "down":
		state.current = min(max(len(state.plan.Items)-1, 0), state.current+1)
	case " ", "space":
		if len(state.plan.Items) > 0 {
			item := state.plan.Items[state.current]
			state.selected[item.ID] = !state.selected[item.ID]
		}
	case "esc":
		m.claudeImport = nil
		m.status = "Claude import cancelled"
	case "enter":
		state.busy = true
		m.status = "Importing Claude settings"
		plan, selected := state.plan, cloneSelection(state.selected)
		return m, func() tea.Msg {
			result, err := claudeimport.Apply(plan, selected)
			env := map[string]string{}
			if err == nil {
				for _, item := range plan.Items {
					if selected[item.ID] && item.Kind == claudeimport.Environment {
						env[item.Name] = item.Value
					}
				}
			}
			return claudeImportDoneEvent{result: result, env: env, err: err}
		}
	}
	return m, nil
}

func cloneSelection(source map[string]bool) map[string]bool {
	result := make(map[string]bool, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}
