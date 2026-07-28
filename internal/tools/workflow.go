package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/lookcorner/go-cli/internal/api"
	"github.com/lookcorner/go-cli/internal/workflow"
)

type workflowTool struct {
	cwd func() string
}

func newWorkflowTool(wsRoot func() string) *workflowTool {
	return &workflowTool{cwd: wsRoot}
}

func (t *workflowTool) Definition() api.ToolDefinition {
	return api.ToolDefinition{
		Type: "function", Name: "workflow",
		Description: "Validate or launch a named Rhai workflow. validate_only checks meta/structure without running. Full execution is not available yet.",
		Parameters: objectSchema(map[string]any{
			"name":          map[string]any{"type": "string", "description": "Workflow name from the catalog"},
			"script_path":   map[string]any{"type": "string", "description": "Path to a .rhai workflow file"},
			"script":        map[string]any{"type": "string", "description": "Inline Rhai workflow source"},
			"validate_only": map[string]any{"type": "boolean", "description": "When true, only validate and return"},
			"args":          map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}},
		}),
	}
}

func (t *workflowTool) Execute(_ context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		Name         string            `json:"name"`
		ScriptPath   string            `json:"script_path"`
		Script       string            `json:"script"`
		ValidateOnly bool              `json:"validate_only"`
		Args         map[string]string `json:"args"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("invalid workflow arguments: %w", err)
	}
	name := strings.TrimSpace(args.Name)
	scriptPath := strings.TrimSpace(args.ScriptPath)
	script := strings.TrimSpace(args.Script)
	provided := 0
	if name != "" {
		provided++
	}
	if scriptPath != "" {
		provided++
	}
	if script != "" {
		provided++
	}
	if provided != 1 {
		return "", errors.New("workflow requires exactly one of name, script_path, or script")
	}

	cwd := ""
	if t != nil && t.cwd != nil {
		cwd = t.cwd()
	}

	var resolved workflow.Resolved
	var err error
	switch {
	case name != "":
		resolved, err = workflow.ResolveByName(cwd, name)
	case scriptPath != "":
		path := scriptPath
		if !filepath.IsAbs(path) && cwd != "" {
			path = filepath.Join(cwd, path)
		}
		resolved, err = workflow.ResolvePath(path)
	default:
		if err := workflow.ValidateScript(script); err != nil {
			return "", err
		}
		resolved = workflow.Resolved{Name: "inline", Source: "inline", Script: script}
	}
	if err != nil {
		return "", err
	}
	if err := workflow.ValidateResolved(resolved); err != nil {
		return "", err
	}
	if args.ValidateOnly {
		return fmt.Sprintf("workflow %q validated (%s)", resolved.Name, resolved.Source), nil
	}
	return "", fmt.Errorf("workflow execution is not available yet; re-run with validate_only=true (resolved %s %q)", resolved.Source, resolved.Name)
}
