package tui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/lookcorner/go-cli/internal/marketplace"
	"github.com/lookcorner/go-cli/internal/plugin"
	"github.com/lookcorner/go-cli/internal/skills"
)

type extensionsTab uint8

const (
	extensionsHooks extensionsTab = iota
	extensionsPlugins
	extensionsMarketplace
	extensionsSkills
)

var extensionTabNames = []string{"Hooks", "Plugins", "Marketplace", "Skills"}

type extensionsState struct {
	tab         extensionsTab
	selected    int
	query       []rune
	searching   bool
	busy        bool
	confirm     *extensionRow
	marketplace []marketplace.ScanResult
	err         string
}

type extensionRow struct {
	key, name, detail, status string
	enabled                   bool
	source, relative          string
}

type extensionsEvent struct {
	marketplace []marketplace.ScanResult
	message     string
	err         error
	loaded      bool
}

func (m *model) openExtensions(name string) tea.Cmd {
	tab := extensionsHooks
	for index, candidate := range extensionTabNames {
		if strings.EqualFold(candidate, name) {
			tab = extensionsTab(index)
			break
		}
	}
	m.extensions = &extensionsState{tab: tab}
	m.scroll = 0
	m.status = "extensions"
	return m.refreshExtensions()
}

func (m *model) refreshExtensions() tea.Cmd {
	state := m.extensions
	if state == nil {
		return nil
	}
	state.err = ""
	if state.tab != extensionsMarketplace {
		state.selected = min(state.selected, max(len(m.extensionRows())-1, 0))
		return nil
	}
	if m.runner == nil || m.runner.MarketplaceList == nil {
		state.err = "Marketplace is unavailable"
		return nil
	}
	state.busy = true
	list := m.runner.MarketplaceList
	return func() tea.Msg {
		items, err := list()
		return extensionsEvent{marketplace: items, err: err, loaded: true}
	}
}

func (m *model) extensionRows() []extensionRow {
	state := m.extensions
	if state == nil {
		return nil
	}
	var rows []extensionRow
	switch state.tab {
	case extensionsHooks:
		if m.runner != nil && m.runner.HookCatalog != nil {
			for _, item := range m.runner.HookCatalog.Snapshot().Hooks {
				detail := string(item.Event) + " · " + item.Type
				if item.Matcher != "" {
					detail += " · " + item.Matcher
				}
				rows = append(rows, extensionRow{key: item.Name, name: item.Name, detail: detail, enabled: !item.Disabled})
			}
		}
	case extensionsPlugins:
		if m.runner != nil && m.runner.PluginInventory != nil {
			for _, item := range m.runner.PluginInventory() {
				key := item.ID
				if key == "" {
					key = item.Name
				}
				detail := item.Scope
				if item.Version != "" {
					detail += " · v" + item.Version
				}
				rows = append(rows, extensionRow{key: key, name: item.Name, detail: detail, enabled: item.Enabled})
			}
		}
	case extensionsMarketplace:
		for _, source := range state.marketplace {
			if source.Error != "" && len(source.Plugins) == 0 {
				rows = append(rows, extensionRow{name: source.SourceName, detail: source.Error, status: "error"})
			}
			for _, item := range source.Plugins {
				detail := source.SourceName
				if item.Description != "" {
					detail += " · " + item.Description
				}
				rows = append(rows, extensionRow{key: item.Name, name: item.Name, detail: detail, status: item.InstallStatus, source: source.SourceURLOrPath, relative: item.RelativePath})
			}
		}
	case extensionsSkills:
		if m.runner != nil && m.runner.Skills != nil {
			for _, item := range m.runner.Skills.List() {
				key := item.Name
				if item.PluginName != "" {
					key = item.PluginName + ":" + item.Name
				}
				detail := item.Scope
				if item.Description != "" {
					detail += " · " + item.Description
				}
				rows = append(rows, extensionRow{key: key, name: item.Name, detail: detail, enabled: item.Enabled})
			}
		}
	}
	query := strings.ToLower(strings.TrimSpace(string(state.query)))
	if query == "" {
		return rows
	}
	filtered := rows[:0]
	for _, row := range rows {
		if matchesExtensionQuery(row.name+" "+row.detail+" "+row.status, query) {
			filtered = append(filtered, row)
		}
	}
	return filtered
}

func (m *model) extensionsContent() string {
	state := m.extensions
	if state == nil {
		return ""
	}
	var out strings.Builder
	out.WriteString("# Extensions\n\n")
	for index, name := range extensionTabNames {
		if extensionsTab(index) == state.tab {
			out.WriteString("[" + name + "] ")
		} else {
			out.WriteString(name + " ")
		}
	}
	out.WriteString("\n\n")
	if state.searching || len(state.query) > 0 {
		out.WriteString("Search: " + string(state.query) + "\n\n")
	}
	if state.busy {
		out.WriteString("Loading...\n")
	}
	rows := m.extensionRows()
	if len(rows) == 0 && !state.busy {
		out.WriteString("No matching items.\n")
	}
	for index, row := range rows {
		cursor := "  "
		if index == state.selected {
			cursor = "> "
		}
		mark := ""
		if state.tab != extensionsMarketplace {
			mark = "[ ] "
			if row.enabled {
				mark = "[x] "
			}
		} else if row.status != "" {
			mark = "[" + strings.ReplaceAll(row.status, "_", " ") + "] "
		}
		out.WriteString(fmt.Sprintf("%s%s%s\n    %s\n", cursor, mark, row.name, row.detail))
	}
	if state.err != "" {
		out.WriteString("\nError: " + state.err + "\n")
	}
	if state.confirm != nil {
		out.WriteString("\nUninstall " + state.confirm.name + "? Press Y to confirm or N to cancel.\n")
	}
	return strings.TrimSpace(out.String())
}

func (m *model) extensionsHint() string {
	state := m.extensions
	if state == nil {
		return ""
	}
	if state.searching {
		return "Type to search | Enter accept | Esc clear"
	}
	if state.confirm != nil {
		return "Y confirm uninstall | N/Esc cancel"
	}
	hint := "Left/Right tabs | Up/Down select | / search | R refresh | Esc close"
	if state.tab == extensionsMarketplace {
		hint = "I install | U update | D uninstall | " + hint
	} else {
		hint = "Enter/Space toggle | " + hint
	}
	return hint
}

func (m *model) handleExtensionsKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	state := m.extensions
	if state == nil || state.busy {
		return m, nil
	}
	stroke := msg.Keystroke()
	text := strings.ToLower(msg.Key().Text)
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
			row := *state.confirm
			state.confirm = nil
			return m, m.marketplaceCommand("uninstall", row)
		}
		if text == "n" || stroke == "esc" {
			state.confirm = nil
		}
		return m, nil
	}
	rows := m.extensionRows()
	switch {
	case stroke == "esc":
		m.extensions = nil
		m.status = "extensions closed"
	case stroke == "left" || stroke == "shift+tab":
		state.tab = extensionsTab((int(state.tab) + len(extensionTabNames) - 1) % len(extensionTabNames))
		state.selected, state.query = 0, nil
		return m, m.refreshExtensions()
	case stroke == "right" || stroke == "tab":
		state.tab = extensionsTab((int(state.tab) + 1) % len(extensionTabNames))
		state.selected, state.query = 0, nil
		return m, m.refreshExtensions()
	case stroke == "up" || text == "k":
		state.selected = max(0, state.selected-1)
	case stroke == "down" || text == "j":
		state.selected = min(max(len(rows)-1, 0), state.selected+1)
	case text == "/":
		state.searching = true
	case text == "r":
		return m, m.reloadExtensions()
	case len(rows) > 0 && state.tab != extensionsMarketplace && (stroke == "enter" || stroke == "space" || text == " "):
		return m, m.toggleExtension(rows[state.selected])
	case len(rows) > 0 && state.tab == extensionsMarketplace && text == "i":
		if rows[state.selected].status != "not_installed" {
			state.err = "Plugin is already installed"
			return m, nil
		}
		return m, m.marketplaceCommand("install", rows[state.selected])
	case len(rows) > 0 && state.tab == extensionsMarketplace && text == "u":
		if rows[state.selected].status != "installed" {
			state.err = "Plugin is not installed"
			return m, nil
		}
		return m, m.marketplaceCommand("update", rows[state.selected])
	case len(rows) > 0 && state.tab == extensionsMarketplace && text == "d":
		if rows[state.selected].status != "installed" {
			state.err = "Plugin is not installed"
			return m, nil
		}
		state.confirm = &rows[state.selected]
	}
	return m, nil
}

func (m *model) toggleExtension(row extensionRow) tea.Cmd {
	state := m.extensions
	state.busy = true
	runner, tab := m.runner, state.tab
	return func() tea.Msg {
		ctx := context.Background()
		var err error
		switch tab {
		case extensionsHooks:
			if runner == nil || runner.HookCatalog == nil {
				err = fmt.Errorf("hooks are unavailable")
			} else {
				err = runner.HookCatalog.SetDisabled(ctx, []string{row.key}, row.enabled)
			}
		case extensionsPlugins:
			if runner == nil || runner.UpdatePlugins == nil {
				err = fmt.Errorf("plugin configuration is read-only")
			} else {
				_, err = runner.UpdatePlugins(ctx, func(settings *plugin.Settings) { setPluginEnabled(settings, row.key, !row.enabled) })
			}
		case extensionsSkills:
			if runner == nil || runner.UpdateSkills == nil {
				err = fmt.Errorf("skill configuration is read-only")
			} else {
				_, err = runner.UpdateSkills(ctx, func(settings *skills.Settings) {
					settings.Disabled = setDisabled(settings.Disabled, row.key, row.enabled)
				})
			}
		}
		return extensionsEvent{message: "Extension state updated", err: err}
	}
}

func (m *model) reloadExtensions() tea.Cmd {
	state := m.extensions
	if state.tab == extensionsMarketplace {
		return m.refreshExtensions()
	}
	state.busy = true
	runner, tab := m.runner, state.tab
	return func() tea.Msg {
		var err error
		switch {
		case runner == nil:
			err = fmt.Errorf("extensions are unavailable")
		case tab == extensionsHooks && runner.ReloadHooks == nil:
			err = fmt.Errorf("hook reload is unavailable")
		case tab == extensionsHooks:
			err = runner.ReloadHooks()
		case tab == extensionsPlugins && runner.UpdatePlugins == nil:
			err = fmt.Errorf("plugin reload is unavailable")
		case tab == extensionsPlugins:
			_, err = runner.UpdatePlugins(context.Background(), nil)
		case tab == extensionsSkills && runner.Skills == nil:
			err = fmt.Errorf("skill reload is unavailable")
		case tab == extensionsSkills:
			err = runner.Skills.Refresh()
		}
		return extensionsEvent{message: "Extensions refreshed", err: err}
	}
}

func (m *model) marketplaceCommand(action string, row extensionRow) tea.Cmd {
	state := m.extensions
	if m.runner == nil || m.runner.MarketplaceAction == nil || row.source == "" || row.relative == "" {
		state.err = "Marketplace action is unavailable"
		return nil
	}
	state.busy = true
	run := m.runner.MarketplaceAction
	return func() tea.Msg {
		outcome, err := run(context.Background(), marketplace.Action{Type: action, SourceURLOrPath: row.source, PluginRelativePath: row.relative})
		if err == nil && outcome.Status != "success" {
			err = fmt.Errorf("%s", outcome.Message)
		}
		return extensionsEvent{message: outcome.Message, err: err}
	}
}

func (m *model) handleExtensionsEvent(event extensionsEvent) (tea.Model, tea.Cmd) {
	state := m.extensions
	if state == nil {
		return m, nil
	}
	state.busy = false
	if event.err != nil {
		state.err = event.err.Error()
		m.status = "extension update failed"
		return m, nil
	}
	if event.loaded {
		state.marketplace = event.marketplace
	}
	state.err = ""
	state.selected = min(state.selected, max(len(m.extensionRows())-1, 0))
	if event.message != "" {
		m.status = event.message
	}
	if state.tab == extensionsMarketplace && !event.loaded {
		return m, m.refreshExtensions()
	}
	return m, nil
}

func setPluginEnabled(settings *plugin.Settings, name string, enabled bool) {
	settings.Enabled = setDisabled(settings.Enabled, name, enabled)
	settings.Disabled = setDisabled(settings.Disabled, name, !enabled)
}

func setDisabled(values []string, name string, disabled bool) []string {
	result := values[:0]
	for _, value := range values {
		if value != name {
			result = append(result, value)
		}
	}
	if disabled {
		result = append(result, name)
	}
	return result
}

func matchesExtensionQuery(value, query string) bool {
	value, query = strings.ToLower(value), strings.ToLower(query)
	if strings.Contains(value, query) {
		return true
	}
	remaining := []rune(query)
	for _, candidate := range value {
		if candidate == remaining[0] {
			remaining = remaining[1:]
			if len(remaining) == 0 {
				return true
			}
		}
	}
	return false
}
