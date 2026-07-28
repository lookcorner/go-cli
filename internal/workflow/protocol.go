package workflow

import "encoding/json"

// NDJSON sidecar protocol between gork (host) and an external Rhai runner.
//
// Host → runner: one start line, then host_reply lines for request kinds that
// expect a response.
// Runner → host: host_request / host_notify lines, then a terminal outcome.

const (
	MsgStart      = "start"
	MsgHostReq    = "host_request"
	MsgHostNotify = "host_notify"
	MsgHostReply  = "host_reply"
	MsgOutcome    = "outcome"
)

// AgentOpts mirrors xai-workflow AgentOpts for spawn_agent.
type AgentOpts struct {
	Prompt            string          `json:"prompt"`
	Label             string          `json:"label,omitempty"`
	Model             string          `json:"model,omitempty"`
	MaxOutputTokens   *uint64         `json:"max_output_tokens,omitempty"`
	AgentType         string          `json:"agent_type,omitempty"`
	CapabilityMode    string          `json:"capability_mode,omitempty"`
	IsolationWorktree bool            `json:"isolation_worktree,omitempty"`
	ForkContext       bool            `json:"fork_context,omitempty"`
	ResumeFrom        string          `json:"resume_from,omitempty"`
	OutputSchema      json.RawMessage `json:"output_schema,omitempty"`
	Phase             string          `json:"phase,omitempty"`
}

// AgentResult mirrors xai-workflow AgentResult.
type AgentResult struct {
	AgentID    string          `json:"agent_id"`
	Success    bool            `json:"success"`
	Output     json.RawMessage `json:"output"`
	Cancelled  bool            `json:"cancelled"`
	TokensUsed uint64          `json:"tokens_used"`
	DurationMS uint64          `json:"duration_ms"`
}

// BudgetState mirrors xai-workflow BudgetState.
type BudgetState struct {
	Total     *uint64 `json:"total"`
	Spent     uint64  `json:"spent"`
	Reserved  uint64  `json:"reserved"`
	Remaining *uint64 `json:"remaining"`
}

// StartMessage is the first stdin line the host sends the runner.
type StartMessage struct {
	Type        string          `json:"type"`
	Script      string          `json:"script"`
	Args        json.RawMessage `json:"args,omitempty"`
	AgentBudget uint64          `json:"agent_budget,omitempty"`
}

// HostRequest is a runner→host call that expects HostReply.
type HostRequest struct {
	Type    string          `json:"type"`
	ID      string          `json:"id"`
	Kind    string          `json:"kind"`
	Count   uint64          `json:"count,omitempty"`
	Opts    *AgentOpts      `json:"opts,omitempty"`
	Name    string          `json:"name,omitempty"`
	Vars    json.RawMessage `json:"vars,omitempty"`
	Content string          `json:"content,omitempty"`
	Commit  string          `json:"commit,omitempty"`
}

// HostNotify is a runner→host one-way event (phase/log/telemetry).
type HostNotify struct {
	Type     string          `json:"type"`
	Kind     string          `json:"kind"`
	Title    string          `json:"title,omitempty"`
	Message  string          `json:"message,omitempty"`
	Name     string          `json:"name,omitempty"`
	Fields   json.RawMessage `json:"fields,omitempty"`
	Replayed bool            `json:"replayed,omitempty"`
}

// HostReply is host→runner response to HostRequest.
type HostReply struct {
	Type   string          `json:"type"`
	ID     string          `json:"id"`
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *HostErrorBody  `json:"error,omitempty"`
}

// HostErrorBody is a structured host failure.
type HostErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// OutcomeMessage is the terminal runner→host line.
type OutcomeMessage struct {
	Type    string          `json:"type"`
	Outcome string          `json:"outcome"` // completed|paused|budget_exceeded|cancelled|failed
	Result  json.RawMessage `json:"result,omitempty"`
	Kind    string          `json:"kind,omitempty"` // pause kind
	Message string          `json:"message,omitempty"`
	Error   string          `json:"error,omitempty"`
}
