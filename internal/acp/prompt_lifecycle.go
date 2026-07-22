package acp

import (
	"encoding/json"
	"errors"

	"github.com/lookcorner/go-cli/internal/agent"
)

type promptLifecycle struct {
	sessionID string
	promptID  string
	turnID    *uint64
}

func (s *Server) failPrompt(incoming message, current *session, lifecycle promptLifecycle, message string) {
	err := errors.New(message)
	if lifecycle.promptID == "" {
		s.respondError(incoming.ID, -32000, message)
		return
	}
	s.finishPrompt(incoming, current, lifecycle, "error", agent.Result{}, err, "")
}

func newPromptLifecycle(request promptRequest) promptLifecycle {
	lifecycle := promptLifecycle{sessionID: request.SessionID, promptID: promptID(request.Meta)}
	if value, ok := request.Meta["turnId"]; ok {
		var turnID uint64
		switch value := value.(type) {
		case float64:
			if value >= 0 && value == float64(uint64(value)) {
				turnID = uint64(value)
				lifecycle.turnID = &turnID
			}
		case json.Number:
			if parsed, err := value.Int64(); err == nil && parsed >= 0 {
				turnID = uint64(parsed)
				lifecycle.turnID = &turnID
			}
		}
	}
	return lifecycle
}

func (s *Server) finishPrompt(incoming message, current *session, lifecycle promptLifecycle, stopReason string, result agent.Result, err error, cancelTrigger string) {
	agentResult := any(nil)
	if err != nil && stopReason != "cancelled" {
		stopReason = "error"
		agentResult = err.Error()
	}
	params := map[string]any{
		"sessionId": lifecycle.sessionID, "promptId": lifecycle.promptID,
		"stopReason": stopReason, "agentResult": agentResult,
	}
	if lifecycle.turnID != nil {
		params["turnId"] = *lifecycle.turnID
	}
	if cancelTrigger != "" {
		params["cancelTrigger"] = cancelTrigger
	}
	s.write(map[string]any{"jsonrpc": "2.0", "method": "x.ai/session/prompt_complete", "params": params})
	if stopReason == "error" {
		s.respondError(incoming.ID, -32000, err.Error())
		return
	}
	s.respond(incoming.ID, promptResponse(current, lifecycle, stopReason, result, cancelTrigger))
}

func promptResponse(current *session, lifecycle promptLifecycle, stopReason string, result agent.Result, cancelTrigger string) map[string]any {
	meta := map[string]any{
		"sessionId": lifecycle.sessionID, "requestId": lifecycle.promptID,
		"promptId": lifecycle.promptID, "totalTokens": result.TokensUsed,
	}
	if current.runner != nil {
		meta["modelId"] = current.runner.Model
	} else {
		meta["modelId"] = ""
	}
	if result.Usage != nil {
		meta["inputTokens"] = result.Usage.InputTokens
		meta["outputTokens"] = result.Usage.OutputTokens
		meta["cachedReadTokens"] = result.Usage.CachedReadTokens
		meta["reasoningTokens"] = result.Usage.ReasoningTokens
	}
	if cancelTrigger != "" {
		meta["cancelTrigger"] = cancelTrigger
	}
	return map[string]any{"stopReason": stopReason, "_meta": meta}
}

func (s *Server) respondQueuedPromptCancelled(current *session, item queuedPrompt) {
	s.respond(item.incoming.ID, promptResponse(current, newPromptLifecycle(item.request), "cancelled", agent.Result{}, ""))
}
