package tui

import (
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/lookcorner/go-cli/internal/agent"
	guides "github.com/lookcorner/go-cli/internal/docs"
	"github.com/lookcorner/go-cli/internal/imagine"
	"github.com/lookcorner/go-cli/internal/skills"
	uitheme "github.com/lookcorner/go-cli/internal/theme"
)

const maxSlashMenuRows = 8

type slashCommandItem struct {
	name        string
	aliases     []string
	placeholder string
	description string
}

type slashSuggestion struct {
	label       string
	match       string
	insert      string
	description string
	chain       bool
	exact       []string
	score       int
}

var slashCommandCatalog = []slashCommandItem{
	{name: "quit", aliases: []string{"exit"}, description: "Exit Gork"},
	{name: "help", description: "Show available commands"},
	{name: "docs", placeholder: "[web|title]", description: "Browse documentation"},
	{name: "home", aliases: []string{"welcome"}, description: "Start a new session"},
	{name: "new", aliases: []string{"clear"}, description: "Start a new session"},
	{name: "fork", placeholder: "[--worktree|--no-worktree] [directive]", description: "Fork this session"},
	{name: "compact", description: "Compact conversation context"},
	{name: "copy", placeholder: "[N]", description: "Copy an assistant response"},
	{name: "find", placeholder: "[text]", description: "Search the transcript"},
	{name: "history", description: "Search prompt history"},
	{name: "voice", description: "Toggle voice dictation"},
	{name: "export", placeholder: "[filename]", description: "Export the conversation"},
	{name: "transcript", aliases: []string{"log"}, description: "View the stored transcript"},
	{name: "context", description: "Show context usage"},
	{name: "minimal", description: "Switch to native scrollback"},
	{name: "fullscreen", aliases: []string{"full"}, description: "Switch to full-screen mode"},
	{name: "model", aliases: []string{"m"}, placeholder: "<name> [effort]", description: "Switch model"},
	{name: "effort", placeholder: "<level>", description: "Set reasoning effort"},
	{name: "always-approve", description: "Toggle always-approve mode"},
	{name: "auto", description: "Toggle automatic permissions"},
	{name: "multiline", aliases: []string{"ml"}, description: "Toggle multiline input"},
	{name: "compact-mode", description: "Toggle compact display"},
	{name: "vim-mode", description: "Toggle Vim navigation"},
	{name: "hooks", description: "Manage hooks"},
	{name: "plugins", description: "Manage plugins"},
	{name: "marketplace", description: "Browse plugin marketplaces"},
	{name: "skills", description: "Manage skills"},
	{name: "share", description: "Share this session"},
	{name: "session-info", aliases: []string{"status", "info"}, description: "Show session details"},
	{name: "rename", aliases: []string{"title"}, placeholder: "<title>", description: "Rename this session"},
	{name: "dashboard", aliases: []string{"sessions", "agents-dashboard"}, description: "Open the agent dashboard"},
	{name: "cd", placeholder: "<path>", description: "Change workspace"},
	{name: "theme", aliases: []string{"t"}, placeholder: "[name]", description: "Change color theme"},
	{name: "feedback", placeholder: "[text]", description: "Send product feedback"},
	{name: "announcements", placeholder: "<hide|show>", description: "Control announcements"},
	{name: "remember", placeholder: "[note]", description: "Save a memory note"},
	{name: "memory", placeholder: "[enable|disable|browse]", description: "Manage session memory"},
	{name: "flush", description: "Save reusable session context"},
	{name: "dream", description: "Consolidate memory"},
	{name: "plan", placeholder: "[description]", description: "Enter plan mode"},
	{name: "view-plan", aliases: []string{"show-plan", "plan-view"}, description: "View the current plan"},
	{name: "resume", description: "Resume another session"},
	{name: "mcps", description: "Manage MCP servers"},
	{name: "btw", placeholder: "<question>", description: "Ask a side question"},
	{name: "recap", description: "Summarize the current session"},
	{name: "terminal-setup", description: "Inspect terminal capabilities"},
	{name: "loop", placeholder: "<interval> <prompt>", description: "Schedule a recurring prompt"},
	{name: "imagine", placeholder: "<description>", description: "Generate an image"},
	{name: "imagine-video", placeholder: "<description>", description: "Generate a video"},
	{name: "timestamps", description: "Toggle timestamps"},
	{name: "timeline", description: "Toggle the timeline"},
	{name: "toggle-mouse-reporting", description: "Toggle terminal mouse reporting"},
	{name: "settings", aliases: []string{"config", "preferences", "prefs"}, description: "Open settings"},
	{name: "privacy", placeholder: "[opt-out]", description: "Show or update privacy settings"},
	{name: "rewind", description: "Rewind to an earlier turn"},
	{name: "jump", description: "Jump to a conversation turn"},
	{name: "login", description: "Sign in"},
	{name: "logout", description: "Sign out"},
	{name: "import-claude", description: "Import Claude settings"},
	{name: "usage", aliases: []string{"cost"}, placeholder: "[show|manage]", description: "Show usage or manage billing"},
	{name: "queue", description: "Show queued prompts"},
	{name: "tasks", description: "Show background tasks"},
	{name: "release-notes", aliases: []string{"changelog"}, description: "Show release notes"},
	{name: "config-agents", aliases: []string{"agents"}, description: "Configure agents"},
	{name: "personas", description: "Configure personas"},
	{name: "debug", placeholder: "[scroll|fps|log]", description: "Inspect TUI diagnostics"},
	{name: "scroll-debug", description: "Toggle scroll diagnostics"},
}

func (m *model) slashSuggestions() []slashSuggestion {
	if m.running || m.rememberInput || m.feedbackInput || m.cursor != len(m.input) {
		return nil
	}
	line := string(m.input)
	if line == "" || !strings.HasPrefix(line, "/") || strings.ContainsRune(line, '\n') || m.slashDismissed == line {
		return nil
	}
	if m.slashQuery != line {
		m.slashQuery = line
		m.slashSelected = 0
	}
	command, arguments, hasArguments := strings.Cut(strings.TrimPrefix(line, "/"), " ")
	if hasArguments {
		query := arguments
		if command == "model" || command == "m" {
			query = m.slashModelArgumentQuery(arguments)
		}
		return rankSlashSuggestions(m.slashArgumentSuggestions(command, arguments), query)
	}
	query := strings.TrimSpace(command)
	items := make([]slashSuggestion, 0, len(slashCommandCatalog))
	for _, item := range slashCommandCatalog {
		if !m.slashCommandAvailable(item.name) {
			continue
		}
		names := append([]string{item.name}, item.aliases...)
		best := slashSuggestion{}
		bestCanonical := false
		for index, name := range names {
			score, ok := slashMatchScore(name, strings.ToLower(query))
			if !ok {
				continue
			}
			insert := "/" + name
			chain := item.placeholder != ""
			if chain {
				insert += " "
			}
			label := "/" + name
			if item.placeholder != "" {
				label += " " + item.placeholder
			}
			candidate := slashSuggestion{
				label: label, match: name, insert: insert, description: item.description,
				chain: chain, exact: []string{"/" + name}, score: score,
			}
			canonical := index == 0
			if best.insert == "" || candidate.score > best.score ||
				candidate.score == best.score && canonical && !bestCanonical ||
				candidate.score == best.score && canonical == bestCanonical && candidate.label < best.label {
				best, bestCanonical = candidate, canonical
			}
		}
		if best.insert != "" {
			items = append(items, best)
		}
	}
	items = append(items, m.slashSkillSuggestions(query)...)
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].score != items[j].score {
			return items[i].score > items[j].score
		}
		return items[i].label < items[j].label
	})
	return items
}

func (m *model) slashModelArgumentQuery(arguments string) string {
	if _, effort, ok := m.slashModelEffortPhase(arguments); ok {
		return effort
	}
	return arguments
}

func (m *model) slashSkillSuggestions(query string) []slashSuggestion {
	if m.runner == nil || m.runner.Skills == nil {
		return nil
	}
	catalog := m.runner.Skills.List()
	counts := make(map[string]int, len(catalog))
	builtins := make(map[string]bool, len(slashCommandCatalog))
	for _, item := range slashCommandCatalog {
		builtins[item.name] = true
		for _, alias := range item.aliases {
			builtins[alias] = true
		}
	}
	for _, item := range catalog {
		if item.Enabled && item.UserInvocable {
			counts[strings.ToLower(item.Name)]++
		}
	}
	items := make([]slashSuggestion, 0, len(catalog))
	for _, item := range catalog {
		if !item.Enabled || !item.UserInvocable {
			continue
		}
		name := item.Name
		key := strings.ToLower(name)
		if counts[key] > 1 || builtins[key] {
			name = qualifiedSlashSkillName(item)
		}
		score, ok := slashMatchScore(strings.ToLower(name), strings.ToLower(query))
		if !ok {
			continue
		}
		label := "/" + name
		if item.ArgumentHint != "" {
			label += " " + item.ArgumentHint
		}
		description := item.ShortDescription
		if description == "" {
			description = item.Description
		}
		items = append(items, slashSuggestion{
			label: label, match: name, insert: "/" + name + " ", description: description,
			chain: true, exact: []string{"/" + name}, score: score,
		})
	}
	return items
}

func qualifiedSlashSkillName(item skills.Info) string {
	if item.PluginName != "" {
		return item.PluginName + ":" + item.Name
	}
	if item.Scope != "" {
		return item.Scope + ":" + item.Name
	}
	return item.Name
}

func (m *model) slashCommandAvailable(name string) bool {
	runner := m.runner
	switch name {
	case "minimal":
		return !m.minimal
	case "fullscreen":
		return m.minimal
	case "always-approve":
		return m.bridge != nil
	case "auto":
		return m.bridge != nil && m.bridge.AutoModeAvailable()
	case "hooks":
		return runner != nil && runner.HookCatalog != nil
	case "plugins":
		return runner != nil && runner.PluginInventory != nil
	case "marketplace":
		return runner != nil && runner.MarketplaceList != nil
	case "skills":
		return runner != nil && runner.Skills != nil
	case "share":
		return runner != nil && runner.SharingEnabled != nil && runner.SharingEnabled()
	case "feedback":
		return runner != nil && runner.SubmitFeedback != nil
	case "announcements":
		return runner != nil && runner.Announcements != nil && runner.Announcements.Available()
	case "mcps":
		return runner != nil && runner.MCPServerCatalog != nil
	case "loop":
		return runner != nil && runner.Tools != nil && runner.Tools.HasTool("scheduler_create")
	case "imagine":
		return runner != nil && runner.Tools != nil && runner.Tools.HasTool(imagine.ImageTool)
	case "imagine-video":
		return runner != nil && runner.Tools != nil && runner.Tools.HasTool(imagine.VideoTool)
	case "voice":
		return m.voiceClient != nil
	case "config-agents":
		return runner != nil && runner.AgentDefinitions != nil
	case "personas":
		return runner != nil && runner.Personas != nil
	default:
		return true
	}
}

func (m *model) slashArgumentSuggestions(command, arguments string) []slashSuggestion {
	command = strings.ToLower(strings.TrimSpace(command))
	prefix := "/" + command + " "
	add := func(values []string, description string) []slashSuggestion {
		items := make([]slashSuggestion, 0, len(values))
		for _, value := range values {
			items = append(items, slashSuggestion{label: value, match: value, insert: prefix + value, description: description, exact: []string{value}})
		}
		return items
	}
	switch command {
	case "model", "m":
		return m.slashModelSuggestions(command, arguments)
	case "effort":
		if m.runner == nil {
			return nil
		}
		items := make([]slashSuggestion, 0, len(m.runner.CurrentReasoningEfforts()))
		for _, effort := range m.runner.CurrentReasoningEfforts() {
			items = append(items, slashSuggestion{label: effort.ID, match: effort.ID, insert: prefix + effort.ID, description: effort.Label, exact: []string{effort.ID, effort.Value}})
		}
		return items
	case "theme", "t":
		return add(append([]string{"auto"}, uitheme.Names[:]...), "Color theme")
	case "docs":
		items := []slashSuggestion{
			{label: "web", match: "web online browser site www", insert: prefix + "web", description: "Open online documentation", exact: []string{"web", "online", "browser", "site", "www"}},
			{label: "how-to", match: "how-to howto guides guide list tui", insert: prefix + "how-to", description: "Show the guide list", exact: []string{"how-to", "howto", "guides", "guide", "list", "tui"}},
		}
		for _, guide := range guides.All() {
			items = append(items, slashSuggestion{label: guide.Title, match: guide.Title, insert: prefix + guide.Title, description: guide.Description, exact: []string{guide.Title}})
		}
		return items
	case "usage", "cost":
		return add([]string{"show", "manage"}, "Usage action")
	case "announcements":
		return add([]string{"hide", "show"}, "Announcement visibility")
	case "privacy":
		return add([]string{"opt-out"}, "Disable data sharing")
	case "memory":
		return add([]string{"enable", "disable", "browse"}, "Memory action")
	case "fork":
		return add([]string{"--worktree", "--no-worktree"}, "Fork workspace mode")
	case "debug":
		return add([]string{"scroll", "fps", "log"}, "Debug panel")
	default:
		return nil
	}
}

func (m *model) slashModelSuggestions(command, arguments string) []slashSuggestion {
	if m.runner == nil {
		return nil
	}
	prefix := "/" + command + " "
	if modelText, _, ok := m.slashModelEffortPhase(arguments); ok {
		for _, option := range m.runner.AvailableModels() {
			if !slashModelMatches(option, modelText) {
				continue
			}
			efforts := option.ReasoningEfforts
			if len(efforts) == 0 {
				if !option.SupportsReasoningEffort {
					return nil
				}
				items := make([]slashSuggestion, 0, 4)
				for _, effort := range []string{"xhigh", "high", "medium", "low"} {
					items = append(items, slashSuggestion{
						label: effort, match: effort, insert: prefix + modelText + " " + effort, description: "Reasoning effort", exact: []string{effort},
					})
				}
				return items
			}
			items := make([]slashSuggestion, 0, len(efforts))
			for _, effort := range efforts {
				items = append(items, slashSuggestion{
					label: effort.ID, match: effort.ID, insert: prefix + modelText + " " + effort.ID, description: effort.Label, exact: []string{effort.ID, effort.Value},
				})
			}
			return items
		}
	}
	items := make([]slashSuggestion, 0, len(m.runner.AvailableModels()))
	for _, option := range m.runner.AvailableModels() {
		insert := prefix + option.ID
		chain := option.SupportsReasoningEffort
		if chain {
			insert += " "
		}
		description := option.Name
		if description == "" {
			description = option.Model
		}
		items = append(items, slashSuggestion{
			label: option.ID, match: option.ID + " " + option.Name, insert: insert, description: description, chain: chain,
			exact: []string{option.ID, option.Name, option.Model},
		})
	}
	return items
}

func (m *model) slashModelEffortPhase(arguments string) (string, string, bool) {
	if m.runner == nil {
		return "", "", false
	}
	matches := func(value string) bool {
		for _, option := range m.runner.AvailableModels() {
			if slashModelMatches(option, value) {
				return true
			}
		}
		return false
	}
	if strings.HasSuffix(arguments, " ") {
		model := strings.TrimSpace(arguments)
		if matches(model) {
			return model, "", true
		}
	}
	for index := len(arguments) - 1; index >= 0; index-- {
		if arguments[index] != ' ' && arguments[index] != '\t' {
			continue
		}
		model := strings.TrimSpace(arguments[:index])
		effort := strings.TrimSpace(arguments[index+1:])
		if model != "" && effort != "" && matches(model) {
			return model, effort, true
		}
	}
	return "", "", false
}

func slashModelMatches(option agent.ModelOption, value string) bool {
	return strings.EqualFold(value, option.ID) || strings.EqualFold(value, option.Name) || strings.EqualFold(value, option.Model)
}

func rankSlashSuggestions(items []slashSuggestion, query string) []slashSuggestion {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return items
	}
	ranked := items[:0]
	for _, item := range items {
		match := item.match
		if match == "" {
			match = item.label
		}
		score, ok := slashMatchScore(strings.ToLower(match), query)
		if !ok {
			continue
		}
		item.score = score
		ranked = append(ranked, item)
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		return ranked[i].label < ranked[j].label
	})
	return ranked
}

func slashMatchScore(value, query string) (int, bool) {
	value = strings.TrimPrefix(value, "/")
	if query == "" {
		return 0, true
	}
	if value == query {
		return 10000, true
	}
	if strings.HasPrefix(value, query) {
		return 8000 - len(value), true
	}
	if index := strings.Index(value, query); index >= 0 {
		return 6000 - index*10 - len(value), true
	}
	position, gaps := 0, 0
	for _, char := range query {
		found := strings.IndexRune(value[position:], char)
		if found < 0 {
			return 0, false
		}
		gaps += found
		position += found + 1
	}
	return 4000 - gaps*10 - len(value), true
}

func (m *model) handleSlashMenuKey(msg tea.KeyPressMsg) (consume, send bool) {
	items := m.slashSuggestions()
	if len(items) == 0 {
		return false, false
	}
	m.slashSelected = min(max(m.slashSelected, 0), len(items)-1)
	switch msg.Keystroke() {
	case "up", "ctrl+p":
		m.slashSelected = (m.slashSelected - 1 + len(items)) % len(items)
		return true, false
	case "down", "ctrl+n":
		m.slashSelected = (m.slashSelected + 1) % len(items)
		return true, false
	case "esc":
		m.slashDismissed = string(m.input)
		return true, false
	case "tab":
		item := items[m.slashSelected]
		m.acceptSlashSuggestion(item)
		if !item.chain {
			m.slashDismissed = string(m.input)
		}
		return true, false
	case "enter":
		item := items[m.slashSelected]
		if strings.TrimSuffix(item.insert, " ") == string(m.input) || slashArgumentExact(string(m.input), item.exact) {
			m.slashDismissed = string(m.input)
			return false, true
		}
		m.acceptSlashSuggestion(item)
		if item.chain {
			return true, false
		}
		m.slashDismissed = string(m.input)
		return false, true
	default:
		return false, false
	}
}

func slashArgumentExact(input string, values []string) bool {
	_, arguments, ok := strings.Cut(input, " ")
	if !ok {
		return false
	}
	arguments = strings.TrimSpace(arguments)
	for _, value := range values {
		if value != "" && strings.EqualFold(arguments, value) {
			return true
		}
	}
	return false
}

func (m *model) acceptSlashSuggestion(item slashSuggestion) {
	m.setInput(item.insert)
	m.slashSelected = 0
	m.slashQuery = item.insert
	m.slashDismissed = ""
}

func (m *model) slashMenuLines(width int) []string {
	items := m.slashSuggestions()
	if len(items) == 0 {
		return nil
	}
	m.slashSelected = min(max(m.slashSelected, 0), len(items)-1)
	rows := min(len(items), m.slashMenuRowLimit())
	start := min(max(m.slashSelected-rows+1, 0), max(len(items)-rows, 0))
	lines := make([]string, 0, rows)
	colors := m.colors()
	for index := start; index < start+rows; index++ {
		prefix := "  "
		if index == m.slashSelected {
			prefix = colors.modal + "› " + ansiReset
		}
		label := items[index].label
		detail := items[index].description
		available := max(width-displayWidth(prefix)-displayWidth(label)-2, 0)
		if available > 0 && detail != "" {
			label += "  " + ansiDim + truncate(detail, available) + ansiReset
		}
		lines = append(lines, truncateANSIUnsafe(prefix+label, width))
	}
	return lines
}

func (m *model) slashMenuRowLimit() int {
	return min(maxSlashMenuRows, max(m.height-7, 1))
}

func (m *model) slashGhost() string {
	items := m.slashSuggestions()
	if len(items) == 0 {
		return ""
	}
	selected := min(max(m.slashSelected, 0), len(items)-1)
	remainder, ok := strings.CutPrefix(items[selected].insert, string(m.input))
	if !ok {
		return ""
	}
	return remainder
}
