package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/lookcorner/go-cli/internal/api"
	"github.com/lookcorner/go-cli/internal/workflow"
)

type workflowTool struct {
	cwd       func() string
	subagents *subagentHolder
}

func newWorkflowTool(wsRoot func() string, subagents *subagentHolder) *workflowTool {
	return &workflowTool{cwd: wsRoot, subagents: subagents}
}

func (t *workflowTool) Definition() api.ToolDefinition {
	return api.ToolDefinition{
		Type: "function", Name: "workflow",
		Description: "Validate or launch a named Rhai workflow. validate_only checks meta/structure without running. Full execution requires GORK_WORKFLOW_RUNNER (or gork-workflow-runner on PATH) and maps agent() to local subagents.",
		Parameters: objectSchema(map[string]any{
			"name":          map[string]any{"type": "string", "description": "Workflow name from the catalog"},
			"script_path":   map[string]any{"type": "string", "description": "Path to a .rhai workflow file"},
			"script":        map[string]any{"type": "string", "description": "Inline Rhai workflow source"},
			"validate_only": map[string]any{"type": "boolean", "description": "When true, only validate and return"},
			"args":          map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}},
		}),
	}
}

type workflowAgentSpawner struct {
	holder *subagentHolder
}

func (s workflowAgentSpawner) SpawnAgent(ctx context.Context, opts workflow.AgentOpts) (workflow.AgentResult, error) {
	if s.holder == nil {
		return workflow.AgentResult{}, errors.New("subagent backend is not initialized")
	}
	backend := s.holder.get()
	if backend == nil {
		return workflow.AgentResult{}, errors.New("subagent backend is not initialized")
	}
	agentType := strings.TrimSpace(opts.AgentType)
	if agentType == "" {
		agentType = "general-purpose"
		if typed, ok := backend.(defaultAgentBackend); ok && typed.DefaultType() != "" {
			agentType = typed.DefaultType()
		}
	}
	isolation := "none"
	if opts.IsolationWorktree {
		isolation = "worktree"
	}
	label := strings.TrimSpace(opts.Label)
	if label == "" {
		label = "workflow-agent"
	}
	start := time.Now()
	result, err := backend.Start(ctx, SubagentRequest{
		Prompt:         opts.Prompt,
		Description:    label,
		Type:           agentType,
		Background:     false,
		BackgroundSet:  true,
		CapabilityMode: strings.TrimSpace(opts.CapabilityMode),
		Isolation:      isolation,
		ResumeFrom:     strings.TrimSpace(opts.ResumeFrom),
		Model:          strings.TrimSpace(opts.Model),
	})
	if err != nil {
		return workflow.AgentResult{}, err
	}
	duration := uint64(time.Since(start).Milliseconds())
	if result.DurationMS > 0 {
		duration = uint64(result.DurationMS)
	}
	output := json.RawMessage(`{}`)
	if text := strings.TrimSpace(result.Output); text != "" {
		if json.Valid([]byte(text)) {
			output = json.RawMessage(text)
		} else {
			encoded, _ := json.Marshal(map[string]any{"text": text})
			output = encoded
		}
	}
	success := !strings.EqualFold(result.Status, "failed") && !strings.EqualFold(result.Status, "error")
	tokens := uint64(0)
	if result.TokensUsed > 0 {
		tokens = uint64(result.TokensUsed)
	}
	return workflow.AgentResult{
		AgentID:    result.ID,
		Success:    success,
		Output:     output,
		Cancelled:  strings.EqualFold(result.Status, "cancelled"),
		TokensUsed: tokens,
		DurationMS: duration,
	}, nil
}

func (t *workflowTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
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
	host := &workflow.Host{Spawner: workflowAgentSpawner{holder: t.subagents}}
	return workflow.Execute(ctx, resolved, args.Args, host)
}
