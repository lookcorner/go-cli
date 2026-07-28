package tui

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/lookcorner/go-cli/internal/workflow"
)

func (m *model) listWorkflowsCatalog() string {
	cwd := ""
	if m != nil {
		cwd = strings.TrimSpace(m.workspace)
	}
	items := workflow.List(cwd)
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
	b.WriteString("\nValidate with `/workflow validate <name|path>`. Execution is not available yet.")
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
		return "Usage: /workflows | /workflow validate <name|path>\nExecution (run/stop/resume) is not available yet."
	}
}
