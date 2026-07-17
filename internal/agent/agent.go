package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/lookcorner/go-cli/internal/api"
	"github.com/lookcorner/go-cli/internal/session"
	"github.com/lookcorner/go-cli/internal/tools"
)

const defaultInstructions = `You are Gork Go, an autonomous coding agent working inside a user-approved workspace.

Inspect relevant files before making changes. Prefer small, focused edits. Use tools to verify your work. Never claim a command, edit, or test succeeded unless its tool result confirms it. All file tools are confined to the workspace; do not try to bypass that boundary. Destructive or system-affecting shell commands require explicit user approval. When the task is complete, summarize the outcome and verification concisely.`

type ResponseStreamer interface {
	StreamResponse(context.Context, api.ResponseRequest, func(string)) (api.StreamResult, error)
}

type EventLogger interface {
	Append(kind string, data any) error
}

type Runner struct {
	Client       ResponseStreamer
	Tools        *tools.Registry
	Logger       EventLogger
	Model        string
	Instructions string
	MaxSteps     int
	TextOutput   io.Writer
	StatusOutput io.Writer
}

type Result struct {
	ResponseID string
	Text       string
	Steps      int
}

func (r *Runner) Run(ctx context.Context, prompt string) (Result, error) {
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

	r.log("user_prompt", map[string]any{"text": prompt})
	input := []api.InputItem{{Type: "message", Role: "user", Content: prompt}}
	previousResponseID := ""
	var final Result

	for step := 1; step <= r.MaxSteps; step++ {
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
		final = Result{ResponseID: streamed.ResponseID, Text: streamed.Text, Steps: step}
		r.log("model_response", map[string]any{
			"step": step, "response_id": streamed.ResponseID,
			"text": streamed.Text, "tool_call_count": len(streamed.ToolCalls),
		})

		if len(streamed.ToolCalls) == 0 {
			return final, nil
		}
		if streamed.ResponseID == "" {
			return final, errors.New("model returned tool calls without a response ID")
		}
		previousResponseID = streamed.ResponseID
		input = make([]api.InputItem, 0, len(streamed.ToolCalls))
		for _, call := range streamed.ToolCalls {
			r.status("tool %s", call.Name)
			r.log("tool_call", map[string]any{
				"step": step, "call_id": call.CallID, "name": call.Name,
				"arguments": json.RawMessage(call.Arguments),
			})
			output, toolErr := r.Tools.Execute(ctx, call.Name, call.Arguments)
			if toolErr != nil {
				output = "ERROR: " + toolErr.Error()
			}
			r.log("tool_result", map[string]any{
				"step": step, "call_id": call.CallID, "name": call.Name,
				"output": output, "failed": toolErr != nil,
			})
			input = append(input, api.InputItem{
				Type: "function_call_output", CallID: call.CallID, Output: output,
			})
		}
	}
	return final, fmt.Errorf("agent reached maximum of %d model steps", r.MaxSteps)
}

func (r *Runner) log(kind string, data any) {
	if r.Logger != nil {
		_ = r.Logger.Append(kind, data)
	}
}

func (r *Runner) status(format string, args ...any) {
	if r.StatusOutput != nil {
		fmt.Fprintf(r.StatusOutput, "\n[gork] "+format+"\n", args...)
	}
}

var _ EventLogger = (*session.Logger)(nil)
