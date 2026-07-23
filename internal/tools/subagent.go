package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/lookcorner/go-cli/internal/api"
)

type SubagentRequest struct {
	Prompt          string
	Description     string
	Type            string
	Background      bool
	BackgroundSet   bool
	CapabilityMode  string
	Isolation       string
	ResumeFrom      string
	CWD             string
	Model           string
	ReasoningEffort string
	HarnessType     string
}

type SubagentResult struct {
	ID            string   `json:"subagent_id"`
	Type          string   `json:"subagent_type"`
	Status        string   `json:"status"`
	Output        string   `json:"output,omitempty"`
	ToolCalls     int      `json:"tool_calls,omitempty"`
	Turns         int      `json:"turns,omitempty"`
	DurationMS    int64    `json:"duration_ms,omitempty"`
	WorktreeDir   string   `json:"worktree_path,omitempty"`
	Description   string   `json:"description,omitempty"`
	StartedAtMS   int64    `json:"started_at_epoch_ms,omitempty"`
	ContextWindow int      `json:"context_window_tokens,omitempty"`
	TokensUsed    int      `json:"tokens_used,omitempty"`
	ContextUsage  int      `json:"context_usage_pct,omitempty"`
	ToolsUsed     []string `json:"tools_used,omitempty"`
	ErrorCount    int      `json:"error_count,omitempty"`
	WillWake      bool     `json:"will_wake,omitempty"`
}

type SubagentBackend interface {
	Description() string
	Start(context.Context, SubagentRequest) (SubagentResult, error)
	Has(string) bool
	Output(context.Context, string, time.Duration) (SubagentResult, error)
	Kill(context.Context, string) (string, error)
}

type defaultAgentBackend interface {
	DefaultType() string
}

type subagentHolder struct {
	mu      sync.RWMutex
	backend SubagentBackend
}

func (h *subagentHolder) set(backend SubagentBackend) {
	h.mu.Lock()
	h.backend = backend
	h.mu.Unlock()
}

func (h *subagentHolder) get() SubagentBackend {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.backend
}

type subagentTool struct{ holder *subagentHolder }

func (t *subagentTool) Definition() api.ToolDefinition {
	description := "Launch a subagent to handle an independent task."
	if backend := t.holder.get(); backend != nil {
		description = backend.Description()
	}
	return api.ToolDefinition{Type: "function", Name: "task", Description: description, Parameters: objectSchema(map[string]any{
		"prompt":            map[string]any{"type": "string", "description": "Full task prompt for the subagent."},
		"description":       map[string]any{"type": "string", "description": "Short task description."},
		"subagent_type":     map[string]any{"type": "string", "default": "general-purpose"},
		"run_in_background": map[string]any{"type": "boolean", "default": true},
		"capability_mode":   map[string]any{"type": "string", "enum": []string{"read-only", "read-write", "execute", "all"}},
		"isolation":         map[string]any{"type": "string", "enum": []string{"none", "worktree"}},
		"resume_from":       map[string]any{"type": "string"},
		"cwd":               map[string]any{"type": "string"},
		"model":             map[string]any{"type": "string"},
		"reasoning_effort":  map[string]any{"type": "string", "enum": []string{"low", "medium", "high", "xhigh", "max"}},
	}, "prompt", "description")}
}

func (t *subagentTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		Prompt          string `json:"prompt"`
		Description     string `json:"description"`
		Type            string `json:"subagent_type"`
		Background      *bool  `json:"run_in_background"`
		CapabilityMode  string `json:"capability_mode"`
		Isolation       string `json:"isolation"`
		ResumeFrom      string `json:"resume_from"`
		CWD             string `json:"cwd"`
		Model           string `json:"model"`
		ReasoningEffort string `json:"reasoning_effort"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("decode task arguments: %w", err)
	}
	if strings.TrimSpace(args.Prompt) == "" || strings.TrimSpace(args.Description) == "" {
		return "", errors.New("task prompt and description are required")
	}
	if args.Type == "" {
		args.Type = "general-purpose"
		if typed, ok := t.holder.get().(defaultAgentBackend); ok && typed.DefaultType() != "" {
			args.Type = typed.DefaultType()
		}
	}
	background := true
	if args.Background != nil {
		background = *args.Background
	}
	backend := t.holder.get()
	if backend == nil {
		return "", errors.New("subagent support is not initialized")
	}
	result, err := backend.Start(ctx, SubagentRequest{
		Prompt: args.Prompt, Description: args.Description, Type: args.Type, Background: background,
		BackgroundSet:  args.Background != nil,
		CapabilityMode: args.CapabilityMode, Isolation: args.Isolation, ResumeFrom: args.ResumeFrom,
		CWD: args.CWD, Model: args.Model,
		ReasoningEffort: args.ReasoningEffort,
	})
	if err != nil {
		return "", err
	}
	encoded, _ := json.Marshal(result)
	return string(encoded), nil
}

func (r *Registry) SetSubagentBackend(backend SubagentBackend) error {
	if backend == nil {
		return errors.New("subagent backend is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.tools["task"]; exists {
		return errors.New("task tool is already registered")
	}
	r.subagents.set(backend)
	r.tools["task"] = &subagentTool{holder: r.subagents}
	return nil
}
