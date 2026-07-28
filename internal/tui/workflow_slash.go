package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/lookcorner/go-cli/internal/tools"
	"github.com/lookcorner/go-cli/internal/workflow"
)

type workflowDoneEvent struct {
	name   string
	result string
	err    error
}

func runWorkflow(ctx context.Context, registry *tools.Registry, name string, args json.RawMessage) tea.Cmd {
	return func() tea.Msg {
		if registry == nil || !registry.HasTool("workflow") {
			return workflowDoneEvent{name: name, err: fmt.Errorf("workflow tool is unavailable")}
		}
		request := map[string]any{"name": name}
		if len(args) > 0 {
			request["args"] = args
		}
		arguments, err := json.Marshal(request)
		if err != nil {
			return workflowDoneEvent{name: name, err: err}
		}
		result, err := registry.Execute(ctx, "workflow", arguments)
		return workflowDoneEvent{name: name, result: result, err: err}
	}
}

func runDeepResearch(ctx context.Context, registry *tools.Registry, query string) tea.Cmd {
	args, _ := json.Marshal(map[string]string{"query": query})
	return runWorkflow(ctx, registry, "deep-research", args)
}

func namedWorkflowArgs(input string) json.RawMessage {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil
	}
	var object map[string]any
	if json.Unmarshal([]byte(input), &object) == nil && object != nil {
		return json.RawMessage(input)
	}
	args, _ := json.Marshal(map[string]string{"query": input, "objective": input})
	return args
}

func workflowLaunchArgs(fields []string) (name, input string, ok bool) {
	if len(fields) == 0 {
		return "", "", false
	}
	operations := []string{"list", "validate", "pause", "resume", "stop", "save"}
	first := strings.ToLower(fields[0])
	if slices.Contains(operations, first) || len(fields) == 2 && slices.Contains(operations[2:], strings.ToLower(fields[1])) {
		return "", "", false
	}
	return fields[0], strings.Join(fields[1:], " "), true
}

func (m *model) startNamedWorkflow(name, input string) (string, tea.Cmd) {
	if m.runner == nil || m.runner.Tools == nil || !m.runner.Tools.HasTool("workflow") {
		m.status = "workflow unavailable"
		return "Workflow launch is unavailable because the workflow tool is disabled.", nil
	}
	cwd := strings.TrimSpace(m.workspace)
	resolved, err := workflow.ResolveByName(cwd, name)
	if err != nil {
		m.status = "workflow unavailable"
		return fmt.Sprintf("Workflow `%s` unavailable: %v", name, err), nil
	}
	if err := workflow.ValidateResolved(resolved); err != nil {
		m.status = "workflow invalid"
		return fmt.Sprintf("Workflow `%s` invalid: %v", name, err), nil
	}
	m.status = "workflow running"
	return fmt.Sprintf("Workflow `%s` started. Its result will appear here when it finishes.", name), runWorkflow(m.ctx, m.runner.Tools, name, namedWorkflowArgs(input))
}

func (m *model) discoverWorkflows(refresh bool) []workflow.Listing {
	if refresh || len(m.workflowCatalog) == 0 {
		m.workflowCatalog = workflow.List(strings.TrimSpace(m.workspace))
	}
	return m.workflowCatalog
}

func (m *model) namedWorkflowCommands() map[string]workflow.Listing {
	if m.runner == nil || m.runner.Tools == nil || !m.runner.Tools.HasTool("workflow") {
		return nil
	}
	taken := make(map[string]bool, len(slashCommandCatalog))
	for _, item := range slashCommandCatalog {
		taken[strings.ToLower(item.name)] = true
		for _, alias := range item.aliases {
			taken[strings.ToLower(alias)] = true
		}
	}
	if m.runner.Skills != nil {
		for _, item := range m.runner.Skills.List() {
			if item.Enabled && item.UserInvocable {
				taken[strings.ToLower(item.Name)] = true
			}
		}
	}
	commands := make(map[string]workflow.Listing)
	for _, item := range m.discoverWorkflows(false) {
		if !taken[item.Name] {
			commands[item.Name] = item
		}
	}
	return commands
}

func (m *model) slashWorkflowSuggestions(query string) []slashSuggestion {
	items := make([]slashSuggestion, 0)
	for name, item := range m.namedWorkflowCommands() {
		score, ok := slashMatchScore(name, strings.ToLower(query))
		if !ok {
			continue
		}
		items = append(items, slashSuggestion{
			label: "/" + name + " <args>", match: name, insert: "/" + name + " ",
			description: "Workflow: " + item.Description, chain: true,
			exact: []string{"/" + name}, score: score,
		})
	}
	return items
}

func (m *model) dynamicWorkflowCommand(command string) (string, bool) {
	name := strings.TrimPrefix(strings.ToLower(command), "/")
	_, ok := m.namedWorkflowCommands()[name]
	return name, ok
}

func (m *model) listWorkflowsCatalog() string {
	items := m.discoverWorkflows(true)
	if len(items) == 0 {
		return "No workflows found. Add `.rhai` scripts under `.grok/workflows/` or `$GROK_HOME/workflows/`."
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Workflows (%d):\n", len(items)))
	for index, item := range items {
		path := ""
		if item.Path != nil {
			path = " — `" + *item.Path + "`"
		}
		b.WriteString(fmt.Sprintf("%d. `%s` (%s)%s\n   %s\n", index+1, item.Name, item.Source, path, item.Description))
	}
	b.WriteString("\nLaunch with `/workflow <name> [JSON args or text]` or its non-conflicting `/name <args>` shortcut. Validate with `/workflow validate <name|path>`. Deep research also has `/deep-research <query>`.")
	return strings.TrimSpace(b.String())
}

func (m *model) validateWorkflowArg(arg string) string {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return "Usage: /workflow validate <name|path>"
	}
	cwd := ""
	if m != nil {
		cwd = strings.TrimSpace(m.workspace)
	}
	var resolved workflow.Resolved
	var err error
	if strings.HasSuffix(strings.ToLower(arg), ".rhai") || strings.Contains(arg, "/") || strings.Contains(arg, string(filepath.Separator)) {
		path := arg
		if !filepath.IsAbs(path) && cwd != "" {
			path = filepath.Join(cwd, path)
		}
		resolved, err = workflow.ResolvePath(path)
	} else {
		resolved, err = workflow.ResolveByName(cwd, arg)
	}
	if err != nil {
		return "Couldn't resolve workflow: " + err.Error()
	}
	if err := workflow.ValidateResolved(resolved); err != nil {
		return "Workflow invalid: " + err.Error()
	}
	return fmt.Sprintf("workflow `%s` validated (%s)", resolved.Name, resolved.Source)
}

func (m *model) handleWorkflowSlash(fields []string) string {
	if len(fields) == 0 {
		return m.listWorkflowsCatalog()
	}
	switch strings.ToLower(fields[0]) {
	case "validate":
		return m.validateWorkflowArg(strings.Join(fields[1:], " "))
	case "list":
		return m.listWorkflowsCatalog()
	default:
		return "Usage: /workflow <name> [JSON args or text] | /workflow validate <name|path> | /deep-research <query>\nWorkflow pause/resume/stop/save and the run dashboard are deferred."
	}
}
