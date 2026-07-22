package agent

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/lookcorner/go-cli/internal/api"
	"github.com/lookcorner/go-cli/internal/hooks"
	"github.com/lookcorner/go-cli/internal/marketplace"
	"github.com/lookcorner/go-cli/internal/mcp"
	"github.com/lookcorner/go-cli/internal/memory"
	"github.com/lookcorner/go-cli/internal/plugin"
	"github.com/lookcorner/go-cli/internal/session"
	"github.com/lookcorner/go-cli/internal/skills"
	"github.com/lookcorner/go-cli/internal/tools"
)

const defaultInstructions = `You are Gork Go, an autonomous coding agent working inside a user-approved workspace.

Inspect relevant files before making changes. Prefer small, focused edits. Use tools to verify your work. Never claim a command, edit, or test succeeded unless its tool result confirms it. All file tools are confined to the workspace; do not try to bypass that boundary. Destructive or system-affecting shell commands require explicit user approval. When the task is complete, summarize the outcome and verification concisely.`

const recapInstruction = `<system-reminder>Write ONE sentence recap body for a user returning from idle. Output ONLY the body; the UI adds the recap label.

Lead with "You asked ..." when the session was mainly questions or review with no landed change. Lead with "We <past-tense verb> ..." when code, config, or documentation changed. If almost nothing happened, say "You had just begun this session."

Include concrete file, behavior, endpoint, flag, or command details from this session in about 25-40 words. Do not add a label, bullets, markdown, extra sentences, or invent work.</system-reminder>`

const sideQuestionInstruction = `<system-reminder>This is a side question from the user. Answer it directly in a single response.

You are a separate lightweight agent. The main agent continues independently. You share its conversation context, but you have no tools and cannot read files, run commands, search, or take actions. There will be no follow-up turn. Never promise to check or do something later. If the answer is not present in the conversation context, say so.</system-reminder>`

var (
	ErrRecapUnavailable = errors.New("no conversation to recap")
	ErrRecapInProgress  = errors.New("recap already in progress")
	ErrRecapSuperseded  = errors.New("recap superseded by a new prompt")
	ErrBtwInProgress    = errors.New("side question already in progress")
	ErrBtwUnavailable   = errors.New("side question requires an active conversation")
)

type ResponseStreamer = api.Streamer

type EventLogger interface {
	Append(kind string, data any) error
	AppendPrompt(text string, content []session.Content) error
	AppendSyntheticPrompt(text string, content []session.Content) error
}

type ToolObserver interface {
	ToolStarted(call api.ToolCall)
	ToolFinished(call api.ToolCall, result tools.ExecutionResult, err error)
}

type HookPolicy interface {
	SessionStarted(context.Context)
	UserPromptSubmitted(context.Context, string)
	BeforeTool(context.Context, api.ToolCall) error
	AfterTool(context.Context, api.ToolCall, tools.ExecutionResult, error)
	Stopped(context.Context, string, error)
	BeforeCompact(context.Context, string)
	AfterCompact(context.Context, string)
}

type HistoryResetter interface {
	ResetHistory(summary string)
}

type HistoryRewinder interface {
	RewindHistory(messages []session.Message)
}

type Runner struct {
	Client                  ResponseStreamer
	Tools                   *tools.Registry
	Skills                  *skills.Catalog
	PluginInventory         func() []plugin.Plugin
	HookCatalog             *hooks.Catalog
	ReloadHooks             func() error
	ListSubagents           func() []tools.SubagentResult
	GetSubagent             func(context.Context, string, time.Duration) (tools.SubagentResult, error)
	KillSubagent            func(context.Context, string) (string, error)
	ListTasks               func() []tools.ProcessSnapshot
	KillTask                func(context.Context, string) (string, error)
	Logger                  EventLogger
	SessionID               string
	SessionPath             string
	Model                   string
	ReasoningEffort         string
	Instructions            string
	MaxSteps                int
	TextOutput              io.Writer
	StatusOutput            io.Writer
	ToolObserver            ToolObserver
	HookPolicy              HookPolicy
	Progress                func(Progress)
	ContextWindow           int
	CompactThresholdPercent int
	TwoPassCompaction       bool
	Memory                  *memory.Store
	MemoryConfig            memory.Config
	OpenMemory              func() (*memory.Store, error)
	UpdateMCPServers        func(context.Context, []mcp.ServerConfig) error
	MCPServers              func() []mcp.ServerConfig
	UpdateSkills            func(context.Context, func(*skills.Settings)) (skills.Settings, error)
	UpdatePlugins           func(context.Context, func(*plugin.Settings)) ([]plugin.Plugin, error)
	MarketplaceList         func() ([]marketplace.ScanResult, error)
	MarketplaceAction       func(context.Context, marketplace.Action) (marketplace.Outcome, error)
	lastInputTokens         int
	pendingSummary          string
	prefireMu               sync.Mutex
	prefire                 *compactionPrefire
	memoryMu                sync.Mutex
	memoryInjected          bool
	memoryFlushArmed        bool
	memoryFlushRunning      bool
	memoryFlushDone         chan struct{}
	memoryFlushCount        uint64
	memoryLastFlush         string
	memoryIdleCancel        context.CancelFunc
	memoryIdleDone          chan struct{}
	memoryDreamCheckCancel  context.CancelFunc
	memoryDreamCheckDone    chan struct{}
	memoryDreamRunning      bool
	memoryDreamDone         chan struct{}
	memorySessionSaved      bool
	hookStart               sync.Once
	promptEpoch             atomic.Uint64
	recapRunning            atomic.Bool
	btwRunning              atomic.Bool
	interjectionMu          sync.Mutex
	interjections           []Interjection
}

type Interjection struct {
	Text    string
	Content []api.ContentPart
}

type compactionPrefire struct {
	done           chan struct{}
	model          string
	inputTokens    int
	lastResponseID string
	note           string
	tail           strings.Builder
}

const (
	compactionPrefireMaxChars = 12_000
	compactionTailMaxBytes    = 128 << 10
)

type Result struct {
	ResponseID    string
	Text          string
	Usage         *api.Usage
	Steps         int
	InputTokens   int
	ContextWindow int
	ToolCalls     int
	TokensUsed    int
	ToolsUsed     []string
	ErrorCount    int
}

type TaskSnapshot struct {
	Subagents []tools.SubagentResult
	Processes []tools.ProcessSnapshot
	Scheduled []tools.ScheduledTaskCreated
}

type Progress struct {
	Turns       int
	ToolCalls   int
	TokensUsed  int
	InputTokens int
	ToolsUsed   []string
	ErrorCount  int
}

func (r *Runner) Run(ctx context.Context, prompt string) (Result, error) {
	return r.RunTurn(ctx, prompt, "")
}

func (r *Runner) RunShell(ctx context.Context, command string) (string, error) {
	if r.Tools == nil {
		return "", errors.New("agent tools are required")
	}
	command = strings.TrimSpace(command)
	if command == "" {
		return "", errors.New("shell command must not be empty")
	}
	arguments, err := json.Marshal(map[string]string{"command": command})
	if err != nil {
		return "", fmt.Errorf("encode shell command: %w", err)
	}
	ctx = r.permissionContext(ctx, "shell", string(arguments))
	return r.Tools.Execute(ctx, "shell", arguments)
}

func (r *Runner) RenameSession(title string) error {
	if r.Logger == nil || strings.TrimSpace(r.SessionID) == "" {
		return errors.New("no active session")
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return errors.New("usage: /rename <new title>")
	}
	return r.Logger.Append("session_title", map[string]any{"title": title})
}

func (r *Runner) ExportSession(filename, cwd string) (string, string, error) {
	if strings.TrimSpace(r.SessionPath) == "" {
		return "", "", errors.New("no active session to export")
	}
	messages, err := session.Transcript(r.SessionPath)
	if err != nil {
		return "", "", err
	}
	content := session.FormatTranscript(messages)
	if strings.TrimSpace(content) == "" {
		return "", "", errors.New("no conversation content to export")
	}
	filename = strings.TrimSpace(filename)
	if filename == "" {
		return content, "", nil
	}
	path, err := expandExportPath(filename, cwd)
	if err != nil {
		return "", "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", "", fmt.Errorf("create export directory: %w", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return "", "", fmt.Errorf("write conversation export: %w", err)
	}
	return content, path, nil
}

func expandExportPath(filename, cwd string) (string, error) {
	if filename == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		filename = home
	} else if strings.HasPrefix(filename, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		filename = filepath.Join(home, strings.TrimPrefix(filename, "~/"))
	}
	if !filepath.IsAbs(filename) {
		if strings.TrimSpace(cwd) == "" {
			return "", errors.New("export working directory is unavailable")
		}
		filename = filepath.Join(cwd, filename)
	}
	return filepath.Clean(filename), nil
}

func (r *Runner) RunTurn(ctx context.Context, prompt, previousResponseID string) (Result, error) {
	return r.runTurn(ctx, prompt, prompt, previousResponseID, false)
}

func (r *Runner) RunSyntheticTurn(ctx context.Context, prompt, previousResponseID string) (Result, error) {
	return r.runTurn(ctx, prompt, prompt, previousResponseID, true)
}

func (r *Runner) RunTurnParts(ctx context.Context, prompt string, parts []api.ContentPart, previousResponseID string) (Result, error) {
	if len(parts) == 0 {
		return Result{}, errors.New("prompt content must not be empty")
	}
	return r.runTurn(ctx, prompt, parts, previousResponseID, false)
}

func (r *Runner) QueueInterjection(text string, content []api.ContentPart) {
	r.interjectionMu.Lock()
	r.interjections = append(r.interjections, Interjection{Text: text, Content: append([]api.ContentPart(nil), content...)})
	r.interjectionMu.Unlock()
}

func (r *Runner) TakeInterjections() []Interjection {
	r.interjectionMu.Lock()
	pending := r.interjections
	r.interjections = nil
	r.interjectionMu.Unlock()
	return pending
}

func (r *Runner) prependInterjections(pending []Interjection) {
	r.interjectionMu.Lock()
	r.interjections = append(pending, r.interjections...)
	r.interjectionMu.Unlock()
}

func (r *Runner) ClearInterjections() {
	r.interjectionMu.Lock()
	r.interjections = nil
	r.interjectionMu.Unlock()
}

// Recap generates a display-only summary without changing model or session history.
func (r *Runner) Recap(ctx context.Context, previousResponseID string) (string, error) {
	if r == nil || r.Client == nil {
		return "", ErrRecapUnavailable
	}
	epoch := r.promptEpoch.Load()
	if previousResponseID == "" && epoch == 0 {
		return "", ErrRecapUnavailable
	}
	if !r.recapRunning.CompareAndSwap(false, true) {
		return "", ErrRecapInProgress
	}
	defer r.recapRunning.Store(false)
	streamer, input, history, err := r.auxiliaryInput(recapInstruction, previousResponseID)
	if err != nil {
		return "", err
	}
	if !history {
		return "", ErrRecapUnavailable
	}

	request := api.ResponseRequest{
		Model: r.Model, Instructions: r.resolvedInstructions(),
		Input:              input,
		PreviousResponseID: previousResponseID, Stream: true,
	}
	response, err := streamer.StreamResponse(ctx, request, nil)
	if err != nil {
		return "", fmt.Errorf("generate recap: %w", err)
	}
	if r.promptEpoch.Load() != epoch {
		return "", ErrRecapSuperseded
	}
	text := cleanRecapText(response.Text)
	if text == "" {
		return "", ErrRecapUnavailable
	}
	return text, nil
}

// SideQuestion answers one question from an isolated history snapshot and never executes tools.
func (r *Runner) SideQuestion(ctx context.Context, question, previousResponseID string) (string, error) {
	question = strings.TrimSpace(question)
	if r == nil || r.Client == nil || strings.TrimSpace(r.SessionID) == "" || strings.TrimSpace(r.SessionPath) == "" {
		return "", ErrBtwUnavailable
	}
	if question == "" {
		return "", errors.New("side question must not be empty")
	}
	if !r.btwRunning.CompareAndSwap(false, true) {
		return "", ErrBtwInProgress
	}
	defer r.btwRunning.Store(false)
	id, err := newBtwID()
	if err != nil {
		return "", err
	}
	entry := session.BtwEntry{
		BtwSessionID: id, ParentSessionID: r.SessionID, AskedAt: time.Now().UTC(),
		Question: question, Model: r.Model,
	}
	streamer, input, history, err := r.auxiliaryInput(sideQuestionInstruction+"\n\n"+question, previousResponseID)
	if err != nil || !history {
		if err == nil {
			err = ErrBtwUnavailable
		}
		entry.Error = err.Error()
		_ = session.AppendBtw(r.SessionPath, entry)
		return "", err
	}
	definitions := []api.ToolDefinition(nil)
	if r.Tools != nil {
		definitions = r.Tools.Definitions()
	}
	request := api.ResponseRequest{
		Model: r.Model, Instructions: r.resolvedInstructions(), Input: input, Tools: definitions,
		ToolChoice: "auto", PreviousResponseID: previousResponseID, Stream: true,
	}
	response, err := streamer.StreamResponse(ctx, request, nil)
	answer := strings.TrimSpace(response.Text)
	if err != nil {
		err = fmt.Errorf("side question model call failed: %w", err)
	} else if answer == "" {
		err = errors.New("no response from model")
	}
	if err != nil {
		entry.Error = err.Error()
		_ = session.AppendBtw(r.SessionPath, entry)
		return "", err
	}
	entry.Answer, entry.Success = answer, true
	_ = session.AppendBtw(r.SessionPath, entry)
	return answer, nil
}

func (r *Runner) auxiliaryInput(prompt, previousResponseID string) (ResponseStreamer, []api.InputItem, bool, error) {
	streamer := r.Client
	input := make([]api.InputItem, 0, 2)
	history := previousResponseID != ""
	if cloner, ok := r.Client.(api.CompactionCloner); ok {
		streamer = cloner.CloneForCompaction(true)
		history = true
	} else if strings.TrimSpace(r.SessionPath) != "" {
		pending, ok, err := session.PendingPrompt(r.SessionPath)
		if err != nil {
			return nil, nil, false, err
		}
		if ok {
			input = append(input, api.InputItem{Type: "message", Role: "user", Content: sessionMessageInput(pending)})
			history = true
		}
	}
	input = append(input, api.InputItem{Type: "message", Role: "user", Content: prompt})
	return streamer, input, history, nil
}

func sessionMessageInput(message session.Message) any {
	if len(message.Content) == 0 {
		return message.Text
	}
	parts := make([]api.ContentPart, 0, len(message.Content))
	for _, content := range message.Content {
		switch content.Type {
		case "text":
			parts = append(parts, api.ContentPart{Type: "input_text", Text: content.Text})
		case "image":
			uri := content.URI
			if content.Data != "" {
				uri = "data:" + content.MimeType + ";base64," + content.Data
			}
			parts = append(parts, api.ContentPart{Type: "input_image", ImageURL: uri})
		}
	}
	return parts
}

func newBtwID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate side question id: %w", err)
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	return fmt.Sprintf("btw-%x-%x-%x-%x-%x", value[:4], value[4:6], value[6:8], value[8:10], value[10:]), nil
}

func (r *Runner) resolvedInstructions() string {
	instructions := strings.TrimSpace(r.Instructions)
	if instructions == "" {
		return defaultInstructions
	}
	return defaultInstructions + "\n\nAdditional user instructions:\n" + instructions
}

func cleanRecapText(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	for _, label := range []string{"Recap \u2014", "Recap\u2014", "Recap -", "Recap:", "recap:", "Session recap:", "Summary:"} {
		if rest, ok := strings.CutPrefix(value, label); ok {
			value = strings.TrimSpace(rest)
			break
		}
	}
	if len(value) >= 2 && (value[0] == '"' && value[len(value)-1] == '"' || value[0] == '\'' && value[len(value)-1] == '\'') {
		value = strings.TrimSpace(value[1 : len(value)-1])
	}
	if len(value) > 1200 {
		end := 1200
		for end > 0 && !utf8.ValidString(value[:end]) {
			end--
		}
		value = strings.TrimSpace(value[:end]) + "\u2026"
	}
	return value
}

func (r *Runner) runTurn(ctx context.Context, prompt string, content any, previousResponseID string, synthetic bool) (final Result, runErr error) {
	if r.Client == nil || r.Tools == nil {
		return Result{}, errors.New("agent client and tools are required")
	}
	if strings.TrimSpace(prompt) == "" {
		return Result{}, errors.New("prompt must not be empty")
	}
	if !synthetic {
		r.promptEpoch.Add(1)
	}
	r.cancelMemoryIdleFlush()
	r.cancelMemoryDreamCheck()
	if r.HookPolicy != nil {
		r.hookStart.Do(func() { r.HookPolicy.SessionStarted(ctx) })
		r.HookPolicy.UserPromptSubmitted(ctx, prompt)
		defer func() {
			reason := "completed"
			if runErr != nil {
				reason = "failed"
			}
			r.HookPolicy.Stopped(ctx, reason, runErr)
		}()
	}
	if r.MaxSteps < 1 {
		r.MaxSteps = 20
	}
	instructions := r.resolvedInstructions()
	r.maybeStartMemoryFlush(ctx, previousResponseID)
	promptTrace, traceable := compactionPromptTrace(prompt, content)
	if !traceable {
		r.clearCompactionPrefire()
	}
	var prefire *compactionPrefire
	if r.shouldCompact(previousResponseID) {
		_, err := r.compact(ctx, previousResponseID, "auto", "")
		if err != nil {
			r.log("compaction_error", map[string]any{"error": err.Error(), "input_tokens": r.lastInputTokens})
		} else {
			previousResponseID = ""
		}
	} else if traceable {
		prefire = r.prefireForTurn(ctx, previousResponseID)
	}
	var compactTrace strings.Builder
	if prefire != nil {
		appendCompactTrace(&compactTrace, "User: "+promptTrace+"\n")
		defer func() {
			if runErr != nil {
				appendCompactTrace(&compactTrace, "Turn error: "+runErr.Error()+"\n")
			}
			r.appendPrefireTail(prefire, compactTrace.String(), final.ResponseID)
		}()
	}
	if err := r.logPrompt(prompt, content, synthetic); err != nil {
		return Result{}, fmt.Errorf("persist user prompt: %w", err)
	}
	content = r.injectMemoryContext(content, prompt, previousResponseID)
	if r.Skills != nil {
		if information := r.Skills.ExpandReferences(prompt, r.SessionID); information != "" {
			switch value := content.(type) {
			case string:
				content = "<user_query>\n" + value + "\n</user_query>\n" + information
			case []api.ContentPart:
				content = append(append([]api.ContentPart(nil), value...), api.ContentPart{Type: "input_text", Text: information})
			}
		}
	}
	summaryPrefix := ""
	if r.pendingSummary != "" {
		summaryPrefix = "Previous conversation summary:\n" + r.pendingSummary + "\n\n"
		r.pendingSummary = ""
	}
	if summaryPrefix != "" {
		switch value := content.(type) {
		case string:
			content = summaryPrefix + value
		case []api.ContentPart:
			content = append([]api.ContentPart{{Type: "input_text", Text: summaryPrefix}}, value...)
		}
	}

	input := []api.InputItem{{Type: "message", Role: "user", Content: content}}
	progress := Progress{}
	var inFlightInterjections []Interjection
	seenTools := make(map[string]bool)
	publish := func() {
		final.Steps, final.ToolCalls = progress.Turns, progress.ToolCalls
		final.InputTokens, final.TokensUsed = progress.InputTokens, progress.TokensUsed
		final.ToolsUsed, final.ErrorCount = append(final.ToolsUsed[:0], progress.ToolsUsed...), progress.ErrorCount
		if r.Progress != nil {
			copy := progress
			copy.ToolsUsed = append([]string(nil), progress.ToolsUsed...)
			r.Progress(copy)
		}
	}
	for step := 1; step <= r.MaxSteps; step++ {
		if r.Skills != nil {
			if reminder := r.Skills.DrainReminder(); reminder != "" {
				input = append(input, api.InputItem{Type: "message", Role: "user", Content: reminder})
			}
		}
		requestInstructions := instructions
		if mode := r.Tools.ModeInstructions(); mode != "" && !strings.Contains(requestInstructions, mode) {
			requestInstructions = strings.TrimSpace(requestInstructions + "\n\n" + mode)
		}
		request := api.ResponseRequest{
			Model:              r.Model,
			Instructions:       requestInstructions,
			Input:              input,
			Tools:              r.Tools.Definitions(),
			ToolChoice:         "auto",
			ParallelToolCalls:  false,
			PreviousResponseID: previousResponseID,
			Stream:             true,
		}
		if r.ReasoningEffort != "" {
			request.Reasoning = &api.ReasoningConfig{Effort: r.ReasoningEffort}
		}
		r.log("model_request", map[string]any{"step": step, "previous_response_id": previousResponseID})
		streamed, err := r.Client.StreamResponse(ctx, request, func(delta string) {
			if r.TextOutput != nil {
				_, _ = io.WriteString(r.TextOutput, delta)
			}
		})
		if err != nil {
			if len(inFlightInterjections) > 0 {
				r.prependInterjections(inFlightInterjections)
			}
			progress.Turns, progress.ErrorCount = step, progress.ErrorCount+1
			publish()
			r.log("model_error", map[string]any{"step": step, "error": err.Error()})
			return final, err
		}
		inFlightInterjections = nil
		usage := streamed.Usage
		final = Result{
			ResponseID: streamed.ResponseID, Text: streamed.Text, Steps: step,
			Usage: &usage, InputTokens: streamed.Usage.InputTokens, ContextWindow: r.ContextWindow,
		}
		progress.Turns, progress.InputTokens = step, streamed.Usage.InputTokens
		tokens := streamed.Usage.TotalTokens
		if tokens == 0 {
			tokens = streamed.Usage.InputTokens + streamed.Usage.OutputTokens
		}
		progress.TokensUsed += tokens
		publish()
		if streamed.Usage.InputTokens > 0 {
			r.lastInputTokens = streamed.Usage.InputTokens
		}
		r.log("model_response", map[string]any{
			"step": step, "response_id": streamed.ResponseID,
			"text": streamed.Text, "tool_call_count": len(streamed.ToolCalls), "usage": streamed.Usage,
		})
		if prefire != nil && streamed.Text != "" {
			appendCompactTrace(&compactTrace, "Assistant: "+streamed.Text+"\n")
		}

		if len(streamed.ToolCalls) == 0 {
			pending := r.TakeInterjections()
			if len(pending) > 0 {
				if step == r.MaxSteps || streamed.ResponseID == "" {
					r.prependInterjections(pending)
					if streamed.ResponseID == "" {
						return final, errors.New("model returned without a response ID before an interjection")
					}
					return final, nil
				}
				previousResponseID = streamed.ResponseID
				input = nil
				if err := r.appendInterjections(&input, pending); err != nil {
					r.prependInterjections(pending)
					return final, err
				}
				inFlightInterjections = pending
				continue
			}
			r.scheduleMemoryIdleFlush(ctx, final.ResponseID)
			r.scheduleMemoryDreamCheck(ctx)
			return final, nil
		}
		if streamed.ResponseID == "" {
			progress.ErrorCount++
			publish()
			return final, errors.New("model returned tool calls without a response ID")
		}
		previousResponseID = streamed.ResponseID
		input = make([]api.InputItem, 0, len(streamed.ToolCalls))
		var imageParts []api.ContentPart
		for _, call := range streamed.ToolCalls {
			progress.ToolCalls++
			if !seenTools[call.Name] {
				seenTools[call.Name] = true
				progress.ToolsUsed = append(progress.ToolsUsed, call.Name)
			}
			publish()
			r.status("tool %s", call.Name)
			r.log("tool_call", map[string]any{
				"step": step, "call_id": call.CallID, "name": call.Name,
				"arguments": json.RawMessage(call.Arguments),
			})
			if prefire != nil {
				appendCompactTrace(&compactTrace, fmt.Sprintf("Tool call %s: %s\n", call.Name, call.Arguments))
			}
			if r.ToolObserver != nil {
				r.ToolObserver.ToolStarted(call)
			}
			toolCtx := tools.WithToolCall(ctx, call.CallID, call.Name)
			toolCtx = r.permissionContext(toolCtx, call.Name, string(call.Arguments))
			var toolResult tools.ExecutionResult
			var toolErr error
			if r.HookPolicy != nil {
				toolErr = r.HookPolicy.BeforeTool(toolCtx, call)
			}
			if toolErr == nil {
				toolResult, toolErr = r.Tools.ExecuteResult(toolCtx, call.Name, call.Arguments)
				if r.HookPolicy != nil {
					r.HookPolicy.AfterTool(toolCtx, call, toolResult, toolErr)
				}
			}
			output := toolResult.Output
			if r.ToolObserver != nil {
				r.ToolObserver.ToolFinished(call, toolResult, toolErr)
			}
			if toolErr != nil {
				progress.ErrorCount++
				publish()
				output = "ERROR: " + toolErr.Error()
			}
			r.log("tool_result", map[string]any{
				"step": step, "call_id": call.CallID, "name": call.Name,
				"output": output, "failed": toolErr != nil, "image_count": len(toolResult.Images),
			})
			if prefire != nil {
				appendCompactTrace(&compactTrace, "Tool result: "+output+"\n")
			}
			input = append(input, api.InputItem{
				Type: "function_call_output", CallID: call.CallID, Output: output,
			})
			if toolErr == nil && len(toolResult.Images) > 0 {
				if len(imageParts) == 0 {
					imageParts = append(imageParts, api.ContentPart{Type: "input_text", Text: "Images returned by file tools."})
				}
				for _, image := range toolResult.Images {
					imageParts = append(imageParts, api.ContentPart{
						Type: "input_image", ImageURL: "data:" + image.MediaType + ";base64," + base64.StdEncoding.EncodeToString(image.Data),
					})
				}
			}
			if toolErr == nil && r.Skills != nil {
				if reminder := r.Skills.Activate(call.Name, call.Arguments); reminder != "" {
					input = append(input, api.InputItem{Type: "message", Role: "user", Content: reminder})
					r.log("skills_activated", map[string]any{"tool": call.Name})
				}
			}
		}
		if len(imageParts) > 0 {
			input = append(input, api.InputItem{Type: "message", Role: "user", Content: imageParts})
		}
		if step < r.MaxSteps {
			pending := r.TakeInterjections()
			if err := r.appendInterjections(&input, pending); err != nil {
				r.prependInterjections(pending)
				return final, err
			}
			inFlightInterjections = pending
		}
	}
	progress.ErrorCount++
	publish()
	return final, fmt.Errorf("agent reached maximum of %d model steps", r.MaxSteps)
}

func (r *Runner) appendInterjections(input *[]api.InputItem, pending []Interjection) error {
	for _, interjection := range pending {
		text := "<user_query>\n" + interjection.Text + "\n</user_query>"
		content := append([]api.ContentPart{{Type: "input_text", Text: text}}, interjection.Content...)
		if err := r.logSyntheticPrompt(interjection.Text, content); err != nil {
			return fmt.Errorf("persist interjection: %w", err)
		}
		*input = append(*input, api.InputItem{Type: "message", Role: "user", Content: content})
	}
	return nil
}

func (r *Runner) shouldCompact(previousResponseID string) bool {
	if previousResponseID == "" || r.ContextWindow <= 0 || r.lastInputTokens <= 0 {
		return false
	}
	threshold := r.ContextWindow * r.CompactThresholdPercent / 100
	return r.lastInputTokens >= threshold
}

func (r *Runner) shouldPrefire(previousResponseID string) bool {
	if !r.TwoPassCompaction || previousResponseID == "" || r.ContextWindow <= 0 || r.lastInputTokens <= 0 {
		return false
	}
	if _, stateful := r.Client.(HistoryResetter); stateful {
		if _, cloneable := r.Client.(api.CompactionCloner); !cloneable {
			return false
		}
	}
	threshold := r.ContextWindow * r.CompactThresholdPercent / 100
	start := r.ContextWindow * max(0, r.CompactThresholdPercent-10) / 100
	return r.lastInputTokens >= start && r.lastInputTokens < threshold
}

func (r *Runner) prefireForTurn(ctx context.Context, previousResponseID string) *compactionPrefire {
	r.prefireMu.Lock()
	if r.prefire != nil {
		prefire := r.prefire
		r.prefireMu.Unlock()
		return prefire
	}
	r.prefireMu.Unlock()
	if !r.shouldPrefire(previousResponseID) {
		return nil
	}
	r.prefireMu.Lock()
	if r.prefire != nil {
		prefire := r.prefire
		r.prefireMu.Unlock()
		return prefire
	}
	prefire := &compactionPrefire{
		done: make(chan struct{}), model: r.Model, inputTokens: r.lastInputTokens, lastResponseID: previousResponseID,
	}
	r.prefire = prefire
	r.prefireMu.Unlock()
	streamer := r.compactionStreamer(true)
	go r.runCompactionPrefire(context.WithoutCancel(ctx), streamer, prefire, previousResponseID)
	return prefire
}

func (r *Runner) runCompactionPrefire(ctx context.Context, streamer ResponseStreamer, prefire *compactionPrefire, previousResponseID string) {
	request := api.ResponseRequest{
		Model:              prefire.model,
		Instructions:       "Create a precise first-stage conversation summary for later hierarchical compaction. Preserve goals, constraints, decisions, code changes, tool results, verification state, unresolved problems, and exact next actions.",
		Input:              []api.InputItem{{Type: "message", Role: "user", Content: "Summarize the conversation prefix. Return only the self-contained handoff note."}},
		PreviousResponseID: previousResponseID, Stream: true,
	}
	result, err := streamer.StreamResponse(ctx, request, nil)
	note := strings.TrimSpace(result.Text)
	if runes := []rune(note); len(runes) > compactionPrefireMaxChars {
		note = string(runes[:compactionPrefireMaxChars])
	}
	r.prefireMu.Lock()
	if r.prefire == prefire && err == nil {
		prefire.note = note
	}
	close(prefire.done)
	r.prefireMu.Unlock()
	outcome := "cached"
	if err != nil {
		outcome = "sample_failed"
	} else if note == "" {
		outcome = "empty_note"
	}
	r.log("compaction_prefire", map[string]any{"outcome": outcome, "input_tokens": prefire.inputTokens})
}

func (r *Runner) appendPrefireTail(prefire *compactionPrefire, value, responseID string) {
	r.prefireMu.Lock()
	defer r.prefireMu.Unlock()
	if r.prefire != prefire {
		return
	}
	appendCompactTrace(&prefire.tail, value)
	if responseID != "" {
		prefire.lastResponseID = responseID
	}
}

func compactionPromptTrace(prompt string, content any) (string, bool) {
	value, ok := content.([]api.ContentPart)
	if !ok {
		return prompt, true
	}
	var text strings.Builder
	for _, part := range value {
		if part.Type != "input_text" {
			return "", false
		}
		text.WriteString(part.Text)
		text.WriteByte('\n')
	}
	return strings.TrimSpace(text.String()), true
}

func appendCompactTrace(target *strings.Builder, value string) {
	remaining := compactionTailMaxBytes - target.Len()
	if remaining <= 0 || value == "" {
		return
	}
	if len(value) > remaining {
		value = value[:remaining]
		for value != "" && !utf8.ValidString(value) {
			value = value[:len(value)-1]
		}
	}
	target.WriteString(value)
}

func (r *Runner) takeCompactionPrefire(ctx context.Context, previousResponseID string) (string, string, bool, error) {
	r.prefireMu.Lock()
	prefire := r.prefire
	r.prefireMu.Unlock()
	if prefire == nil {
		return "", "", false, nil
	}
	select {
	case <-ctx.Done():
		return "", "", false, ctx.Err()
	case <-prefire.done:
	}
	r.prefireMu.Lock()
	defer r.prefireMu.Unlock()
	if r.prefire != prefire {
		return "", "", false, nil
	}
	r.prefire = nil
	if prefire.model != r.Model || prefire.lastResponseID != previousResponseID || strings.TrimSpace(prefire.note) == "" {
		return "", "", false, nil
	}
	return prefire.note, strings.Clone(prefire.tail.String()), true, nil
}

func (r *Runner) clearCompactionPrefire() {
	r.prefireMu.Lock()
	r.prefire = nil
	r.prefireMu.Unlock()
}

func (r *Runner) compactionStreamer(includeHistory bool) ResponseStreamer {
	if cloner, ok := r.Client.(api.CompactionCloner); ok {
		return cloner.CloneForCompaction(includeHistory)
	}
	return r.Client
}

func (r *Runner) Compact(ctx context.Context, previousResponseID string) (string, error) {
	return r.compact(ctx, previousResponseID, "manual", "")
}

func (r *Runner) CompactWithContext(ctx context.Context, previousResponseID, userContext string) (string, error) {
	return r.compact(ctx, previousResponseID, "manual", userContext)
}

func (r *Runner) compact(ctx context.Context, previousResponseID, source, userContext string) (string, error) {
	if previousResponseID == "" {
		return "", errors.New("no completed response is available to compact")
	}
	if r.HookPolicy != nil {
		r.HookPolicy.BeforeCompact(ctx, source)
	}
	r.maybeStartMemoryFlush(ctx, previousResponseID)
	r.resetMemoryFlushCycle()
	request := api.ResponseRequest{
		Model:        r.Model,
		Instructions: "Create a precise successor-agent handoff summary. Preserve the user's goals, decisions, constraints, modified files, tool results, verification state, unresolved problems, and exact next actions. Do not claim unfinished work is complete.",
		Input: []api.InputItem{{
			Type: "message", Role: "user",
			Content: compactionRequest("Summarize the conversation so a fresh agent context can continue without losing important implementation state.", userContext),
		}},
		PreviousResponseID: previousResponseID, Stream: true,
	}
	note, tail, twoPass, err := r.takeCompactionPrefire(ctx, previousResponseID)
	if err != nil {
		return "", err
	}
	if twoPass {
		request.PreviousResponseID = ""
		request.Input[0].Content = compactionRequest("This is the final pass of hierarchical compaction. Merge the entire prior summary with the recent conversation into one self-contained successor-agent handoff.\n\n<summary_content>\n"+note+"\n</summary_content>\n\n<recent_conversation>\n"+tail+"\n</recent_conversation>", userContext)
	}
	streamer := r.Client
	if twoPass {
		streamer = r.compactionStreamer(false)
	}
	result, err := streamer.StreamResponse(ctx, request, nil)
	usedTwoPass := twoPass
	if (err != nil || strings.TrimSpace(result.Text) == "") && twoPass {
		r.log("compaction_prefire", map[string]any{"outcome": "pass2_failed"})
		request.PreviousResponseID = previousResponseID
		request.Input[0].Content = compactionRequest("Summarize the conversation so a fresh agent context can continue without losing important implementation state.", userContext)
		result, err = r.Client.StreamResponse(ctx, request, nil)
		usedTwoPass = false
	}
	if err != nil {
		return "", err
	}
	summary := strings.TrimSpace(result.Text)
	if summary == "" {
		return "", errors.New("compaction returned an empty summary")
	}
	r.lastInputTokens = 0
	r.pendingSummary = summary
	if resetter, ok := r.Client.(HistoryResetter); ok {
		resetter.ResetHistory(summary)
	}
	r.log("context_compacted", map[string]any{"summary": summary, "two_pass": usedTwoPass})
	r.status("context compacted")
	if r.HookPolicy != nil {
		r.HookPolicy.AfterCompact(ctx, source)
	}
	return summary, nil
}

func compactionRequest(prompt, userContext string) string {
	if userContext = strings.TrimSpace(userContext); userContext != "" {
		return prompt + "\n\n<user_provided_context>\n" + userContext + "\n</user_provided_context>\n\nIncorporate the user-provided context above into the summary."
	}
	return prompt
}

func (r *Runner) RewindHistory(messages []session.Message) {
	r.lastInputTokens = 0
	r.pendingSummary = ""
	r.clearCompactionPrefire()
	if rewinder, ok := r.Client.(HistoryRewinder); ok {
		rewinder.RewindHistory(messages)
	}
}

func (r *Runner) TaskSnapshot() TaskSnapshot {
	if r == nil {
		return TaskSnapshot{}
	}
	var snapshot TaskSnapshot
	if r.ListSubagents != nil {
		snapshot.Subagents = r.ListSubagents()
	}
	if r.ListTasks != nil {
		snapshot.Processes = r.ListTasks()
	}
	if r.Tools != nil {
		snapshot.Scheduled = r.Tools.ScheduledTasks()
	}
	return snapshot
}

func (r *Runner) log(kind string, data any) {
	if r.Logger != nil {
		_ = r.Logger.Append(kind, data)
	}
}

func (r *Runner) logPrompt(text string, value any, synthetic bool) error {
	if r.Logger == nil {
		return nil
	}
	if synthetic {
		return r.Logger.Append("user_prompt", map[string]any{"text": text, "synthetic": true})
	}
	parts, ok := value.([]api.ContentPart)
	if !ok {
		return r.Logger.AppendPrompt(text, nil)
	}
	content := make([]session.Content, 0, len(parts))
	for _, part := range parts {
		switch part.Type {
		case "input_text":
			content = append(content, session.Content{Type: "text", Text: part.Text})
		case "input_image":
			content = append(content, session.Content{Type: "image", URI: part.ImageURL})
		default:
			return fmt.Errorf("unsupported prompt content type %q", part.Type)
		}
	}
	return r.Logger.AppendPrompt(text, content)
}

func (r *Runner) logSyntheticPrompt(text string, parts []api.ContentPart) error {
	if r.Logger == nil {
		return nil
	}
	content := make([]session.Content, 0, len(parts))
	for _, part := range parts {
		switch part.Type {
		case "input_text":
			content = append(content, session.Content{Type: "text", Text: part.Text})
		case "input_image":
			content = append(content, session.Content{Type: "image", URI: part.ImageURL})
		default:
			return fmt.Errorf("unsupported interjection content type %q", part.Type)
		}
	}
	return r.Logger.AppendSyntheticPrompt(text, content)
}

func (r *Runner) status(format string, args ...any) {
	if r.StatusOutput != nil {
		fmt.Fprintf(r.StatusOutput, "\n[gork] "+format+"\n", args...)
	}
}

var _ EventLogger = (*session.Logger)(nil)
