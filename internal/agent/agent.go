package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/lookcorner/go-cli/internal/api"
	"github.com/lookcorner/go-cli/internal/hooks"
	"github.com/lookcorner/go-cli/internal/marketplace"
	"github.com/lookcorner/go-cli/internal/mcp"
	"github.com/lookcorner/go-cli/internal/plugin"
	"github.com/lookcorner/go-cli/internal/session"
	"github.com/lookcorner/go-cli/internal/skills"
	"github.com/lookcorner/go-cli/internal/tools"
)

const defaultInstructions = `You are Gork Go, an autonomous coding agent working inside a user-approved workspace.

Inspect relevant files before making changes. Prefer small, focused edits. Use tools to verify your work. Never claim a command, edit, or test succeeded unless its tool result confirms it. All file tools are confined to the workspace; do not try to bypass that boundary. Destructive or system-affecting shell commands require explicit user approval. When the task is complete, summarize the outcome and verification concisely.`

type ResponseStreamer = api.Streamer

type EventLogger interface {
	Append(kind string, data any) error
	AppendPrompt(text string, content []session.Content) error
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
	hookStart               sync.Once
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
	Steps         int
	InputTokens   int
	ContextWindow int
	ToolCalls     int
	TokensUsed    int
	ToolsUsed     []string
	ErrorCount    int
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

func (r *Runner) runTurn(ctx context.Context, prompt string, content any, previousResponseID string, synthetic bool) (final Result, runErr error) {
	if r.Client == nil || r.Tools == nil {
		return Result{}, errors.New("agent client and tools are required")
	}
	if strings.TrimSpace(prompt) == "" {
		return Result{}, errors.New("prompt must not be empty")
	}
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
	instructions := strings.TrimSpace(r.Instructions)
	if instructions == "" {
		instructions = defaultInstructions
	} else {
		instructions = defaultInstructions + "\n\nAdditional user instructions:\n" + instructions
	}
	promptTrace, traceable := compactionPromptTrace(prompt, content)
	if !traceable {
		r.clearCompactionPrefire()
	}
	var prefire *compactionPrefire
	if r.shouldCompact(previousResponseID) {
		_, err := r.compact(ctx, previousResponseID, "auto")
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
			progress.Turns, progress.ErrorCount = step, progress.ErrorCount+1
			publish()
			r.log("model_error", map[string]any{"step": step, "error": err.Error()})
			return final, err
		}
		final = Result{
			ResponseID: streamed.ResponseID, Text: streamed.Text, Steps: step,
			InputTokens: streamed.Usage.InputTokens, ContextWindow: r.ContextWindow,
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
	}
	progress.ErrorCount++
	publish()
	return final, fmt.Errorf("agent reached maximum of %d model steps", r.MaxSteps)
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
	return r.compact(ctx, previousResponseID, "manual")
}

func (r *Runner) compact(ctx context.Context, previousResponseID, source string) (string, error) {
	if previousResponseID == "" {
		return "", errors.New("no completed response is available to compact")
	}
	if r.HookPolicy != nil {
		r.HookPolicy.BeforeCompact(ctx, source)
	}
	request := api.ResponseRequest{
		Model:        r.Model,
		Instructions: "Create a precise successor-agent handoff summary. Preserve the user's goals, decisions, constraints, modified files, tool results, verification state, unresolved problems, and exact next actions. Do not claim unfinished work is complete.",
		Input: []api.InputItem{{
			Type: "message", Role: "user",
			Content: "Summarize the conversation so a fresh agent context can continue without losing important implementation state.",
		}},
		PreviousResponseID: previousResponseID, Stream: true,
	}
	note, tail, twoPass, err := r.takeCompactionPrefire(ctx, previousResponseID)
	if err != nil {
		return "", err
	}
	if twoPass {
		request.PreviousResponseID = ""
		request.Input[0].Content = "This is the final pass of hierarchical compaction. Merge the entire prior summary with the recent conversation into one self-contained successor-agent handoff.\n\n<summary_content>\n" + note + "\n</summary_content>\n\n<recent_conversation>\n" + tail + "\n</recent_conversation>"
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
		request.Input[0].Content = "Summarize the conversation so a fresh agent context can continue without losing important implementation state."
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

func (r *Runner) RewindHistory(messages []session.Message) {
	r.lastInputTokens = 0
	r.pendingSummary = ""
	r.clearCompactionPrefire()
	if rewinder, ok := r.Client.(HistoryRewinder); ok {
		rewinder.RewindHistory(messages)
	}
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

func (r *Runner) status(format string, args ...any) {
	if r.StatusOutput != nil {
		fmt.Fprintf(r.StatusOutput, "\n[gork] "+format+"\n", args...)
	}
}

var _ EventLogger = (*session.Logger)(nil)
