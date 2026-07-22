package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/api"
	"github.com/lookcorner/go-cli/internal/hooks"
	"github.com/lookcorner/go-cli/internal/tools"
)

type clientHookResult struct {
	decision string
	reason   string
}

func parseClientHooks(meta map[string]any) []hooks.ClientHookGroup {
	configured, _ := meta["x.ai/hooks"].(map[string]any)
	var result []hooks.ClientHookGroup
	for event, rawGroups := range configured {
		groups, _ := rawGroups.([]any)
		for _, rawGroup := range groups {
			encoded, err := json.Marshal(rawGroup)
			if err != nil {
				continue
			}
			var wire struct {
				Matcher     string   `json:"matcher"`
				CallbackIDs []string `json:"hookCallbackIds"`
				Timeout     float64  `json:"timeout"`
			}
			if json.Unmarshal(encoded, &wire) != nil || len(wire.CallbackIDs) == 0 {
				continue
			}
			timeout := time.Duration(0)
			if wire.Timeout > 0 {
				timeout = time.Duration(min(wire.Timeout, 300) * float64(time.Second))
			}
			if group, ok := hooks.NewClientHookGroup(event, wire.Matcher, wire.CallbackIDs, timeout); ok {
				result = append(result, group)
			}
		}
	}
	return result
}

func (s *Server) attachClientHooks(runner *agent.Runner, groups []hooks.ClientHookGroup, transcriptPath, cwd, sessionID string) {
	if runtime, ok := runner.HookPolicy.(*hooks.Runtime); ok {
		runtime.TranscriptPath = transcriptPath
	}
	if len(groups) == 0 {
		return
	}
	dispatcher := hooks.ClientDispatcher(s.dispatchClientHook)
	runner.HookCatalog = hooks.AttachClient(runner.HookCatalog, groups, dispatcher)
	if runtime, ok := runner.HookPolicy.(*hooks.Runtime); ok {
		runtime.Catalog = runner.HookCatalog
		return
	}
	clientRuntime := &hooks.Runtime{
		Catalog: hooks.AttachClient(nil, groups, dispatcher), WorkspaceRoot: cwd,
		SessionID: sessionID, TranscriptPath: transcriptPath, Model: runner.Model,
	}
	if runner.HookPolicy == nil {
		clientRuntime.Catalog = runner.HookCatalog
		runner.HookPolicy = clientRuntime
		return
	}
	runner.HookPolicy = hookPolicyChain{runner.HookPolicy, clientRuntime}
}

func (s *Server) dispatchClientHook(ctx context.Context, callbackID string, envelope map[string]any, blocking bool) (string, string) {
	params := make(map[string]any, len(envelope)+1)
	for key, value := range envelope {
		params[key] = value
	}
	params["hookCallbackId"] = callbackID
	if !blocking {
		s.write(map[string]any{"jsonrpc": "2.0", "method": "x.ai/hooks/event", "params": params})
		return "", ""
	}
	id := fmt.Sprintf("gork-hook-%d", s.nextRequest.Add(1))
	result := make(chan clientHookResult, 1)
	s.mu.Lock()
	if s.pendingHook == nil {
		s.pendingHook = make(map[string]chan clientHookResult)
	}
	s.pendingHook[id] = result
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.pendingHook, id)
		s.mu.Unlock()
	}()
	s.write(map[string]any{"jsonrpc": "2.0", "id": id, "method": "x.ai/hooks/run", "params": params})
	select {
	case response := <-result:
		return response.decision, response.reason
	case <-ctx.Done():
		return "", ""
	}
}

func handleClientHookResponse(pending chan clientHookResult, incoming message) {
	result := clientHookResult{}
	if len(incoming.Error) == 0 || string(incoming.Error) == "null" {
		var response struct {
			Decision      string `json:"decision"`
			SystemMessage string `json:"systemMessage"`
		}
		if json.Unmarshal(incoming.Result, &response) == nil && response.Decision == "deny" {
			result = clientHookResult{decision: "deny", reason: response.SystemMessage}
		}
	}
	select {
	case pending <- result:
	default:
	}
}

type hookPolicyChain struct {
	first  agent.HookPolicy
	second agent.HookPolicy
}

func (p hookPolicyChain) SessionStarted(ctx context.Context) {
	p.first.SessionStarted(ctx)
	p.second.SessionStarted(ctx)
}

func (p hookPolicyChain) UserPromptSubmitted(ctx context.Context, prompt string) {
	p.first.UserPromptSubmitted(ctx, prompt)
	p.second.UserPromptSubmitted(ctx, prompt)
}

func (p hookPolicyChain) BeforeTool(ctx context.Context, call api.ToolCall) error {
	if err := p.first.BeforeTool(ctx, call); err != nil {
		return err
	}
	return p.second.BeforeTool(ctx, call)
}

func (p hookPolicyChain) AfterTool(ctx context.Context, call api.ToolCall, result tools.ExecutionResult, err error) {
	p.first.AfterTool(ctx, call, result, err)
	p.second.AfterTool(ctx, call, result, err)
}

func (p hookPolicyChain) Stopped(ctx context.Context, reason string, err error) {
	p.first.Stopped(ctx, reason, err)
	p.second.Stopped(ctx, reason, err)
}

func (p hookPolicyChain) BeforeCompact(ctx context.Context, source string) {
	p.first.BeforeCompact(ctx, source)
	p.second.BeforeCompact(ctx, source)
}

func (p hookPolicyChain) AfterCompact(ctx context.Context, source string) {
	p.first.AfterCompact(ctx, source)
	p.second.AfterCompact(ctx, source)
}
