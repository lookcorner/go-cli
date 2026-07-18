package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/lookcorner/go-cli/internal/api"
	"github.com/lookcorner/go-cli/internal/mcp"
	"github.com/lookcorner/go-cli/internal/plugin"
	"github.com/lookcorner/go-cli/internal/session"
	"github.com/lookcorner/go-cli/internal/skills"
	"github.com/lookcorner/go-cli/internal/tools"
)

const defaultInstructions = `You are Gork Go, an autonomous coding agent working inside a user-approved workspace.

Inspect relevant files before making changes. Prefer small, focused edits. Use tools to verify your work. Never claim a command, edit, or test succeeded unless its tool result confirms it. All file tools are confined to the workspace; do not try to bypass that boundary. Destructive or system-affecting shell commands require explicit user approval. When the task is complete, summarize the outcome and verification concisely.`

type ResponseStreamer interface {
	StreamResponse(context.Context, api.ResponseRequest, func(string)) (api.StreamResult, error)
}

type EventLogger interface {
	Append(kind string, data any) error
	AppendPrompt(text string, content []session.Content) error
}

type ToolObserver interface {
	ToolStarted(call api.ToolCall)
	ToolFinished(call api.ToolCall, result tools.ExecutionResult, err error)
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
	Plugins                 []plugin.Plugin
	Logger                  EventLogger
	SessionID               string
	Model                   string
	Instructions            string
	MaxSteps                int
	TextOutput              io.Writer
	StatusOutput            io.Writer
	ToolObserver            ToolObserver
	ContextWindow           int
	CompactThresholdPercent int
	UpdateMCPServers        func(context.Context, []mcp.ServerConfig) error
	MCPServers              func() []mcp.ServerConfig
	UpdateSkills            func(context.Context, func(*skills.Settings)) (skills.Settings, error)
	lastInputTokens         int
	pendingSummary          string
}

type Result struct {
	ResponseID    string
	Text          string
	Steps         int
	InputTokens   int
	ContextWindow int
}

func (r *Runner) Run(ctx context.Context, prompt string) (Result, error) {
	return r.RunTurn(ctx, prompt, "")
}

func (r *Runner) RunTurn(ctx context.Context, prompt, previousResponseID string) (Result, error) {
	return r.runTurn(ctx, prompt, prompt, previousResponseID)
}

func (r *Runner) RunTurnParts(ctx context.Context, prompt string, parts []api.ContentPart, previousResponseID string) (Result, error) {
	if len(parts) == 0 {
		return Result{}, errors.New("prompt content must not be empty")
	}
	return r.runTurn(ctx, prompt, parts, previousResponseID)
}

func (r *Runner) runTurn(ctx context.Context, prompt string, content any, previousResponseID string) (Result, error) {
	if r.Client == nil || r.Tools == nil {
		return Result{}, errors.New("agent client and tools are required")
	}
	if strings.TrimSpace(prompt) == "" {
		return Result{}, errors.New("prompt must not be empty")
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
	if r.shouldCompact(previousResponseID) {
		_, err := r.Compact(ctx, previousResponseID)
		if err != nil {
			r.log("compaction_error", map[string]any{"error": err.Error(), "input_tokens": r.lastInputTokens})
		} else {
			previousResponseID = ""
		}
	}
	if err := r.logPrompt(prompt, content); err != nil {
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
	var final Result

	for step := 1; step <= r.MaxSteps; step++ {
		if r.Skills != nil {
			if reminder := r.Skills.DrainReminder(); reminder != "" {
				input = append(input, api.InputItem{Type: "message", Role: "user", Content: reminder})
			}
		}
		request := api.ResponseRequest{
			Model:              r.Model,
			Instructions:       instructions,
			Input:              input,
			Tools:              r.Tools.Definitions(),
			ToolChoice:         "auto",
			ParallelToolCalls:  false,
			PreviousResponseID: previousResponseID,
			Stream:             true,
		}
		r.log("model_request", map[string]any{"step": step, "previous_response_id": previousResponseID})
		streamed, err := r.Client.StreamResponse(ctx, request, func(delta string) {
			if r.TextOutput != nil {
				_, _ = io.WriteString(r.TextOutput, delta)
			}
		})
		if err != nil {
			r.log("model_error", map[string]any{"step": step, "error": err.Error()})
			return final, err
		}
		final = Result{
			ResponseID: streamed.ResponseID, Text: streamed.Text, Steps: step,
			InputTokens: streamed.Usage.InputTokens, ContextWindow: r.ContextWindow,
		}
		if streamed.Usage.InputTokens > 0 {
			r.lastInputTokens = streamed.Usage.InputTokens
		}
		r.log("model_response", map[string]any{
			"step": step, "response_id": streamed.ResponseID,
			"text": streamed.Text, "tool_call_count": len(streamed.ToolCalls), "usage": streamed.Usage,
		})

		if len(streamed.ToolCalls) == 0 {
			return final, nil
		}
		if streamed.ResponseID == "" {
			return final, errors.New("model returned tool calls without a response ID")
		}
		previousResponseID = streamed.ResponseID
		input = make([]api.InputItem, 0, len(streamed.ToolCalls))
		var imageParts []api.ContentPart
		for _, call := range streamed.ToolCalls {
			r.status("tool %s", call.Name)
			r.log("tool_call", map[string]any{
				"step": step, "call_id": call.CallID, "name": call.Name,
				"arguments": json.RawMessage(call.Arguments),
			})
			if r.ToolObserver != nil {
				r.ToolObserver.ToolStarted(call)
			}
			toolCtx := tools.WithToolCall(ctx, call.CallID, call.Name)
			toolResult, toolErr := r.Tools.ExecuteResult(toolCtx, call.Name, call.Arguments)
			output := toolResult.Output
			if r.ToolObserver != nil {
				r.ToolObserver.ToolFinished(call, toolResult, toolErr)
			}
			if toolErr != nil {
				output = "ERROR: " + toolErr.Error()
			}
			r.log("tool_result", map[string]any{
				"step": step, "call_id": call.CallID, "name": call.Name,
				"output": output, "failed": toolErr != nil, "image_count": len(toolResult.Images),
			})
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
	return final, fmt.Errorf("agent reached maximum of %d model steps", r.MaxSteps)
}

func (r *Runner) shouldCompact(previousResponseID string) bool {
	if previousResponseID == "" || r.ContextWindow <= 0 || r.lastInputTokens <= 0 {
		return false
	}
	threshold := r.ContextWindow * r.CompactThresholdPercent / 100
	return r.lastInputTokens >= threshold
}

func (r *Runner) Compact(ctx context.Context, previousResponseID string) (string, error) {
	if previousResponseID == "" {
		return "", errors.New("no completed response is available to compact")
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
	result, err := r.Client.StreamResponse(ctx, request, nil)
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
	r.log("context_compacted", map[string]any{"summary": summary})
	r.status("context compacted")
	return summary, nil
}

func (r *Runner) RewindHistory(messages []session.Message) {
	r.lastInputTokens = 0
	r.pendingSummary = ""
	if rewinder, ok := r.Client.(HistoryRewinder); ok {
		rewinder.RewindHistory(messages)
	}
}

func (r *Runner) log(kind string, data any) {
	if r.Logger != nil {
		_ = r.Logger.Append(kind, data)
	}
}

func (r *Runner) logPrompt(text string, value any) error {
	if r.Logger == nil {
		return nil
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
