package tui

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/lookcorner/go-cli/internal/agent"
	mcppkg "github.com/lookcorner/go-cli/internal/mcp"
)

type mcpPhase uint8

const (
	mcpServers mcpPhase = iota
	mcpTools
	mcpAddSource
	mcpAddName
	mcpDelete
)

type mcpModal struct {
	phase    mcpPhase
	servers  []mcppkg.ServerConfig
	selected int
	server   string
	filter   int
	source   string
	input    []rune
	cursor   int
	busy     bool
	err      string
}

type mcpTool struct {
	name, title, description string
	enabled                  bool
}

type mcpDoneEvent struct {
	action string
	err    error
}

type mcpToolIdentity interface {
	MCPIdentity() (string, string, mcppkg.ToolInfo)
}

func newMCPModal(runner *agent.Runner) *mcpModal {
	state := &mcpModal{}
	state.refresh(runner)
	return state
}

func (s *mcpModal) refresh(runner *agent.Runner) {
	s.servers = nil
	if runner != nil && runner.MCPServerCatalog != nil {
		for _, server := range runner.MCPServerCatalog() {
			if s.filter == 1 && server.Disabled || s.filter == 2 && !server.Disabled {
				continue
			}
			s.servers = append(s.servers, server)
		}
	}
	sort.Slice(s.servers, func(i, j int) bool { return s.servers[i].Name < s.servers[j].Name })
	if len(s.servers) == 0 {
		s.selected = 0
	} else {
		s.selected = min(s.selected, len(s.servers)-1)
	}
}

func (s *mcpModal) tools(runner *agent.Runner) []mcpTool {
	byName := make(map[string]mcpTool)
	if runner != nil && runner.Tools != nil {
		for _, registered := range runner.Tools.SnapshotTools() {
			identity, ok := registered.(mcpToolIdentity)
			if !ok {
				continue
			}
			server, name, info := identity.MCPIdentity()
			if server == s.server {
				byName[name] = mcpTool{name: name, title: info.Title, description: info.Description, enabled: true}
			}
		}
	}
	for _, server := range s.servers {
		if server.Name != s.server {
			continue
		}
		for _, name := range server.DisabledTools {
			tool := byName[name]
			tool.name, tool.enabled = name, false
			byName[name] = tool
		}
		break
	}
	result := make([]mcpTool, 0, len(byName))
	for _, tool := range byName {
		result = append(result, tool)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].name < result[j].name })
	return result
}

func (m *model) openMCPModal() {
	if m.runner == nil || m.runner.MCPServerCatalog == nil {
		m.appendSystem("MCP server management is unavailable")
		m.status = "MCP unavailable"
		return
	}
	m.mcp = newMCPModal(m.runner)
	m.scroll = 0
	m.status = "MCP servers"
}

func (m *model) handleMCPKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	state := m.mcp
	key, stroke := msg.Key(), msg.Keystroke()
	if stroke == "ctrl+q" {
		return m, tea.Quit
	}
	if state.busy {
		return m, nil
	}
	if state.phase == mcpAddSource || state.phase == mcpAddName {
		if key.Code == tea.KeyEsc {
			if state.phase == mcpAddName {
				state.phase, state.input, state.cursor = mcpAddSource, []rune(state.source), len([]rune(state.source))
				m.status = "enter MCP URL or command"
			} else {
				state.phase, state.input, state.cursor = mcpServers, nil, 0
				m.status = "MCP servers"
			}
			state.err = ""
			return m, nil
		}
		if key.Code == tea.KeyEnter {
			value := strings.TrimSpace(string(state.input))
			if state.phase == mcpAddSource {
				if value == "" {
					state.err = "URL or command is required"
					return m, nil
				}
				state.source, state.phase, state.input, state.cursor = value, mcpAddName, nil, 0
				state.err = ""
				m.status = "enter optional MCP server name"
				return m, nil
			}
			server, err := mcppkg.ParseServerInput(state.source, value)
			if err != nil {
				state.err = err.Error()
				return m, nil
			}
			if m.runner.UpsertMCPServer == nil {
				state.err = "MCP configuration is read-only"
				return m, nil
			}
			state.busy, state.err = true, ""
			return m, runMCPAction("server added", func(ctx context.Context) error { return m.runner.UpsertMCPServer(ctx, server) }, m.ctx)
		}
		switch key.Code {
		case tea.KeyBackspace:
			if state.cursor > 0 {
				state.input = append(state.input[:state.cursor-1], state.input[state.cursor:]...)
				state.cursor--
			}
		case tea.KeyDelete:
			if state.cursor < len(state.input) {
				state.input = append(state.input[:state.cursor], state.input[state.cursor+1:]...)
			}
		case tea.KeyLeft:
			state.cursor = max(0, state.cursor-1)
		case tea.KeyRight:
			state.cursor = min(len(state.input), state.cursor+1)
		case tea.KeyHome:
			state.cursor = 0
		case tea.KeyEnd:
			state.cursor = len(state.input)
		default:
			if key.Text != "" && key.Mod == 0 {
				chars := []rune(key.Text)
				state.input = slices.Insert(state.input, state.cursor, chars...)
				state.cursor += len(chars)
			}
		}
		return m, nil
	}
	if state.phase == mcpDelete {
		if key.Code == tea.KeyEsc || strings.EqualFold(key.Text, "n") {
			state.phase, state.err = mcpServers, ""
			m.status = "MCP servers"
			return m, nil
		}
		if key.Code == tea.KeyEnter || strings.EqualFold(key.Text, "y") {
			if m.runner.DeleteMCPServer == nil {
				state.err = "MCP configuration is read-only"
				return m, nil
			}
			name := state.server
			state.busy, state.err = true, ""
			return m, runMCPAction("server deleted", func(ctx context.Context) error { return m.runner.DeleteMCPServer(ctx, name) }, m.ctx)
		}
		return m, nil
	}
	if key.Code == tea.KeyEsc {
		if state.phase == mcpTools {
			state.phase, state.selected, state.server = mcpServers, 0, ""
			m.status = "MCP servers"
		} else {
			m.mcp = nil
			m.status = "ready"
		}
		return m, nil
	}
	items := len(state.servers)
	if state.phase == mcpTools {
		items = len(state.tools(m.runner))
	}
	if key.Code == tea.KeyUp || key.Text == "k" {
		state.selected = max(0, state.selected-1)
		return m, nil
	}
	if key.Code == tea.KeyDown || key.Text == "j" {
		state.selected = min(max(items-1, 0), state.selected+1)
		return m, nil
	}
	if state.phase == mcpTools {
		if (key.Code == tea.KeySpace || key.Text == " ") && items > 0 {
			tools := state.tools(m.runner)
			tool := tools[state.selected]
			if m.runner.ToggleMCPTool == nil {
				state.err = "MCP tool configuration is read-only"
				return m, nil
			}
			server := state.server
			state.busy, state.err = true, ""
			return m, runMCPAction("tool updated", func(ctx context.Context) error { return m.runner.ToggleMCPTool(ctx, server, tool.name, !tool.enabled) }, m.ctx)
		}
		return m, nil
	}
	switch {
	case key.Code == tea.KeyEnter && items > 0:
		state.phase, state.server, state.selected = mcpTools, state.servers[state.selected].Name, 0
		m.status = "MCP tools"
	case key.Code == tea.KeySpace || key.Text == " ":
		if items == 0 {
			break
		}
		server := state.servers[state.selected]
		if m.runner.ToggleMCPServer == nil {
			state.err = "MCP configuration is read-only"
			break
		}
		state.busy, state.err = true, ""
		return m, runMCPAction("server updated", func(ctx context.Context) error { return m.runner.ToggleMCPServer(ctx, server.Name, server.Disabled) }, m.ctx)
	case strings.EqualFold(key.Text, "a"):
		if m.runner.UpsertMCPServer == nil {
			state.err = "MCP configuration is read-only"
			break
		}
		state.phase, state.input, state.cursor, state.err = mcpAddSource, nil, 0, ""
		m.status = "enter MCP URL or command"
	case strings.EqualFold(key.Text, "x") && items > 0:
		state.phase, state.server, state.err = mcpDelete, state.servers[state.selected].Name, ""
		m.status = "confirm MCP server deletion"
	case strings.EqualFold(key.Text, "r"):
		if m.runner.ReloadMCPBase == nil {
			state.err = "MCP reload is unavailable"
			break
		}
		state.busy, state.err = true, ""
		return m, runMCPAction("servers reloaded", m.runner.ReloadMCPBase, m.ctx)
	case strings.EqualFold(key.Text, "f"):
		state.filter, state.selected = (state.filter+1)%3, 0
		state.refresh(m.runner)
		m.status = "MCP filter: " + []string{"all", "enabled", "disabled"}[state.filter]
	case strings.EqualFold(key.Text, "i"):
		state.err = "OAuth is not supported for local MCP servers"
	}
	return m, nil
}

func runMCPAction(action string, apply func(context.Context) error, ctx context.Context) tea.Cmd {
	return func() tea.Msg { return mcpDoneEvent{action: action, err: apply(ctx)} }
}

func (m *model) mcpContent() string {
	state := m.mcp
	if state == nil {
		return ""
	}
	if state.phase == mcpAddSource || state.phase == mcpAddName {
		title := "Add MCP server"
		label := "URL or command"
		if state.phase == mcpAddName {
			label = "Name (optional)"
			title += "\n\n**Source:** " + mcpText(state.source)
		}
		content := "# " + title + "\n\n**" + label + ":**\n\n> " + mcpText(string(state.input))
		if state.err != "" {
			content += "\n\n**Error:** " + mcpText(state.err)
		}
		return content
	}
	if state.phase == mcpDelete {
		content := "# Remove MCP server\n\nRemove **" + mcpText(state.server) + "** from user configuration?"
		if state.err != "" {
			content += "\n\n**Error:** " + mcpText(state.err)
		}
		return content
	}
	if state.phase == mcpTools {
		tools := state.tools(m.runner)
		lines := make([]string, 0, len(tools))
		for _, tool := range tools {
			label := tool.title
			if label == "" {
				label = tool.name
			}
			status := "off"
			if tool.enabled {
				status = "on"
			}
			line := fmt.Sprintf("[%s] %s", status, mcpText(label))
			if tool.description != "" {
				line += " - " + mcpText(tool.description)
			}
			lines = append(lines, line)
		}
		content := "# MCP tools: " + mcpText(state.server) + "\n\n"
		if len(lines) == 0 {
			content += "No tools reported. Enable or reload the server to discover tools."
		} else {
			content += selectedLines(lines, state.selected)
		}
		if state.err != "" {
			content += "\n\n**Error:** " + mcpText(state.err)
		}
		return content
	}
	lines := make([]string, 0, len(state.servers))
	for _, server := range state.servers {
		status := "on"
		if server.Disabled {
			status = "off"
		}
		source := server.URL
		if source == "" {
			source = strings.TrimSpace(strings.Join(append([]string{server.Command}, server.Args...), " "))
		}
		lines = append(lines, fmt.Sprintf("[%s] %s - %s", status, mcpText(server.Name), mcpText(source)))
	}
	filter := []string{"all", "enabled", "disabled"}[state.filter]
	content := fmt.Sprintf("# MCP servers\n\nFilter: **%s**\n\n", filter)
	if len(lines) == 0 {
		content += "No MCP servers configured."
	} else {
		content += selectedLines(lines, state.selected)
	}
	if state.err != "" {
		content += "\n\n**Error:** " + mcpText(state.err)
	}
	return content
}

func (m *model) mcpHint() string {
	state := m.mcp
	if state == nil {
		return ""
	}
	if state.busy {
		return "Updating MCP servers..."
	}
	switch state.phase {
	case mcpAddSource:
		return "Enter continue | Esc cancel"
	case mcpAddName:
		return "Enter save | Esc back"
	case mcpDelete:
		return "Y/Enter remove | N/Esc cancel"
	case mcpTools:
		return "Up/Down select | Space toggle | Esc back"
	default:
		return "Up/Down select | Enter tools | Space toggle | A add | X remove | R reload | F filter | I auth | Esc close"
	}
}

func mcpText(value string) string {
	value = strings.ReplaceAll(sanitizeTerminalText(value), "\n", " ")
	return strings.NewReplacer("\\", "\\\\", "`", "\\`", "*", "\\*", "_", "\\_", "[", "\\[", "]", "\\]").Replace(value)
}
