package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	DefaultAgentBudget = 128
	MaxAgentBudget     = 1024

	envWorkflowRunner = "GORK_WORKFLOW_RUNNER"
)

// ErrRunnerUnavailable means no external Rhai runner binary is configured.
var ErrRunnerUnavailable = errors.New("workflow runner unavailable: set GORK_WORKFLOW_RUNNER to a Rhai sidecar binary (or install gork-workflow-runner on PATH)")

// ErrBuiltinScriptMissing means a catalog builtin has no embedded script yet.
var ErrBuiltinScriptMissing = errors.New("builtin workflow script is not embedded; use script_path or a project/user .rhai")

// AgentSpawner launches child agents for host spawn_agent calls.
type AgentSpawner interface {
	SpawnAgent(ctx context.Context, opts AgentOpts) (AgentResult, error)
}

// HostCallbacks receives optional progress events from the runner.
type HostCallbacks struct {
	OnPhase     func(title string, replayed bool)
	OnLog       func(message string, replayed bool)
	OnTelemetry func(name string, fields json.RawMessage, replayed bool)
}

// Host drives runner host requests against local agent/scratch helpers.
type Host struct {
	Spawner   AgentSpawner
	Callbacks HostCallbacks
	Scratch   string // directory for scratch files; empty = temp under os.TempDir

	mu          sync.Mutex
	agentCalls  uint64
	agentBudget uint64
	scratchDir  string
	scratchOnce sync.Once
	scratchErr  error
}

func (h *Host) ensureScratch() (string, error) {
	h.scratchOnce.Do(func() {
		if strings.TrimSpace(h.Scratch) != "" {
			h.scratchDir = h.Scratch
			h.scratchErr = os.MkdirAll(h.scratchDir, 0o700)
			return
		}
		h.scratchDir, h.scratchErr = os.MkdirTemp("", "gork-workflow-scratch-*")
	})
	return h.scratchDir, h.scratchErr
}

func (h *Host) budget() uint64 {
	if h.agentBudget == 0 {
		return DefaultAgentBudget
	}
	if h.agentBudget > MaxAgentBudget {
		return MaxAgentBudget
	}
	return h.agentBudget
}

func (h *Host) HandleRequest(ctx context.Context, req HostRequest) HostReply {
	reply := HostReply{Type: MsgHostReply, ID: req.ID}
	switch req.Kind {
	case "reserve_agent_calls":
		h.mu.Lock()
		requested := h.agentCalls + req.Count
		max := h.budget()
		if requested > max {
			h.mu.Unlock()
			reply.OK = false
			reply.Error = &HostErrorBody{
				Code:    "agent_call_quota_exceeded",
				Message: fmt.Sprintf("workflow agent-call quota exceeded: requested %d, maximum %d", requested, max),
			}
			return reply
		}
		h.agentCalls = requested
		h.mu.Unlock()
		reply.OK = true
		reply.Result = json.RawMessage(`null`)
	case "release_agent_calls":
		h.mu.Lock()
		if req.Count >= h.agentCalls {
			h.agentCalls = 0
		} else {
			h.agentCalls -= req.Count
		}
		h.mu.Unlock()
		reply.OK = true
		reply.Result = json.RawMessage(`null`)
	case "spawn_agent":
		if h.Spawner == nil {
			reply.OK = false
			reply.Error = &HostErrorBody{Code: "failed", Message: "subagent backend is not initialized"}
			return reply
		}
		opts := AgentOpts{}
		if req.Opts != nil {
			opts = *req.Opts
		}
		result, err := h.Spawner.SpawnAgent(ctx, opts)
		if err != nil {
			reply.OK = false
			reply.Error = &HostErrorBody{Code: "failed", Message: err.Error()}
			return reply
		}
		encoded, err := json.Marshal(result)
		if err != nil {
			reply.OK = false
			reply.Error = &HostErrorBody{Code: "failed", Message: err.Error()}
			return reply
		}
		reply.OK = true
		reply.Result = encoded
	case "budget":
		max := h.budget()
		h.mu.Lock()
		spent := h.agentCalls
		h.mu.Unlock()
		remaining := max - spent
		state := BudgetState{Total: &max, Spent: spent, Reserved: spent, Remaining: &remaining}
		encoded, _ := json.Marshal(state)
		reply.OK = true
		reply.Result = encoded
	case "render_template":
		reply.OK = false
		reply.Error = &HostErrorBody{Code: "unsupported", Message: "unsupported in this context: render_template"}
	case "write_scratch_file":
		dir, err := h.ensureScratch()
		if err != nil {
			reply.OK = false
			reply.Error = &HostErrorBody{Code: "failed", Message: err.Error()}
			return reply
		}
		name := filepath.Base(strings.TrimSpace(req.Name))
		if name == "" || name == "." || name == ".." {
			reply.OK = false
			reply.Error = &HostErrorBody{Code: "failed", Message: "invalid scratch file name"}
			return reply
		}
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(req.Content), 0o600); err != nil {
			reply.OK = false
			reply.Error = &HostErrorBody{Code: "failed", Message: err.Error()}
			return reply
		}
		encoded, _ := json.Marshal(path)
		reply.OK = true
		reply.Result = encoded
	case "read_scratch_file":
		dir, err := h.ensureScratch()
		if err != nil {
			reply.OK = false
			reply.Error = &HostErrorBody{Code: "failed", Message: err.Error()}
			return reply
		}
		name := filepath.Base(strings.TrimSpace(req.Name))
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			reply.OK = false
			reply.Error = &HostErrorBody{Code: "failed", Message: err.Error()}
			return reply
		}
		encoded, _ := json.Marshal(string(data))
		reply.OK = true
		reply.Result = encoded
	case "git_diff_since":
		reply.OK = false
		reply.Error = &HostErrorBody{Code: "unsupported", Message: "unsupported in this context: git_diff_since"}
	default:
		reply.OK = false
		reply.Error = &HostErrorBody{Code: "unsupported", Message: "unsupported host request: " + req.Kind}
	}
	return reply
}

func (h *Host) HandleNotify(n HostNotify) {
	switch n.Kind {
	case "phase":
		if h.Callbacks.OnPhase != nil {
			h.Callbacks.OnPhase(n.Title, n.Replayed)
		}
	case "log":
		if h.Callbacks.OnLog != nil {
			h.Callbacks.OnLog(n.Message, n.Replayed)
		}
	case "telemetry":
		if h.Callbacks.OnTelemetry != nil {
			h.Callbacks.OnTelemetry(n.Name, n.Fields, n.Replayed)
		}
	}
}

// ResolveRunnerBinary finds the external Rhai sidecar.
func ResolveRunnerBinary() (string, error) {
	if path := strings.TrimSpace(os.Getenv(envWorkflowRunner)); path != "" {
		if st, err := os.Stat(path); err != nil || st.IsDir() {
			return "", fmt.Errorf("%w: %s", ErrRunnerUnavailable, path)
		}
		return path, nil
	}
	if path, err := lookPath("gork-workflow-runner"); err == nil && path != "" {
		return path, nil
	}
	return "", ErrRunnerUnavailable
}

// FormatOutcome turns a terminal outcome into a tool result string.
func FormatOutcome(msg OutcomeMessage) (string, error) {
	switch msg.Outcome {
	case "completed":
		if len(msg.Result) == 0 {
			return "workflow completed", nil
		}
		return fmt.Sprintf("workflow completed: %s", string(msg.Result)), nil
	case "paused":
		return "", fmt.Errorf("workflow paused (%s): %s", strings.TrimSpace(msg.Kind), strings.TrimSpace(msg.Message))
	case "budget_exceeded":
		return "", fmt.Errorf("workflow token budget exceeded: %s", strings.TrimSpace(msg.Message))
	case "cancelled":
		return "", errors.New("workflow cancelled")
	case "failed":
		errText := strings.TrimSpace(msg.Error)
		if errText == "" {
			errText = strings.TrimSpace(msg.Message)
		}
		if errText == "" {
			errText = "unknown failure"
		}
		return "", fmt.Errorf("workflow failed: %s", errText)
	default:
		return "", fmt.Errorf("workflow returned unknown outcome %q", msg.Outcome)
	}
}
