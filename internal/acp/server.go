package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/api"
	"github.com/lookcorner/go-cli/internal/tools"
)

const ProtocolVersion = 1

type SessionConfig struct {
	CWD        string
	MCPServers []MCPServer
}

type MCPServer struct {
	Name    string
	Command string
	Args    []string
	Env     map[string]string
}

type Factory func(context.Context, SessionConfig, tools.Approver, io.Writer, io.Writer) (*agent.Runner, func(), error)

type Server struct {
	Factory     Factory
	input       io.Reader
	output      io.Writer
	writeMu     sync.Mutex
	mu          sync.Mutex
	sessions    map[string]*session
	pending     map[string]chan permissionResult
	nextSession atomic.Uint64
	nextRequest atomic.Uint64
	wg          sync.WaitGroup
}

type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
}

type session struct {
	id       string
	runner   *agent.Runner
	close    func()
	mu       sync.Mutex
	previous string
	cancel   context.CancelFunc
	running  bool
}

type permissionResult struct {
	outcome  string
	optionID string
	err      error
}

func (s *Server) Serve(ctx context.Context, input io.Reader, output io.Writer) error {
	if s.Factory == nil {
		return errors.New("ACP session factory is required")
	}
	s.input, s.output = input, output
	s.sessions = make(map[string]*session)
	s.pending = make(map[string]chan permissionResult)
	decoder := json.NewDecoder(input)
	for {
		var incoming message
		if err := decoder.Decode(&incoming); err != nil {
			if errors.Is(err, io.EOF) {
				s.closeAll()
				s.wg.Wait()
				return nil
			}
			s.closeAll()
			return fmt.Errorf("decode ACP message: %w", err)
		}
		if incoming.Method == "" && len(incoming.ID) > 0 {
			s.handleClientResponse(incoming)
			continue
		}
		switch incoming.Method {
		case "initialize":
			s.respond(incoming.ID, map[string]any{
				"protocolVersion": ProtocolVersion,
				"agentCapabilities": map[string]any{
					"loadSession":         false,
					"promptCapabilities":  map[string]any{"image": false, "audio": false, "embeddedContext": false},
					"mcpCapabilities":     map[string]any{"http": false, "sse": false},
					"sessionCapabilities": map[string]any{"close": map[string]any{}},
					"auth":                map[string]any{},
				},
				"authMethods": []any{},
				"agentInfo":   map[string]any{"name": "gork-go", "version": "0.1.0-dev"},
			})
		case "session/new":
			s.handleNewSession(ctx, incoming)
		case "session/prompt":
			s.handlePrompt(ctx, incoming)
		case "session/cancel":
			s.handleCancel(incoming.Params)
		case "session/close":
			s.handleClose(incoming)
		default:
			if len(incoming.ID) > 0 {
				s.respondError(incoming.ID, -32601, "method not found")
			}
		}
	}
}

func (s *Server) handleNewSession(ctx context.Context, incoming message) {
	var params struct {
		CWD        string `json:"cwd"`
		MCPServers []struct {
			Name    string   `json:"name"`
			Command string   `json:"command"`
			Args    []string `json:"args"`
			Env     []struct {
				Name  string `json:"name"`
				Value string `json:"value"`
			} `json:"env"`
		} `json:"mcpServers"`
	}
	if json.Unmarshal(incoming.Params, &params) != nil || params.CWD == "" {
		s.respondError(incoming.ID, -32602, "cwd is required")
		return
	}
	id := fmt.Sprintf("gork-%d", s.nextSession.Add(1))
	sessionConfig := SessionConfig{CWD: params.CWD, MCPServers: make([]MCPServer, 0, len(params.MCPServers))}
	for _, remote := range params.MCPServers {
		if remote.Name == "" || remote.Command == "" {
			s.respondError(incoming.ID, -32602, "stdio MCP servers require name and command")
			return
		}
		env := make(map[string]string, len(remote.Env))
		for _, entry := range remote.Env {
			env[entry.Name] = entry.Value
		}
		sessionConfig.MCPServers = append(sessionConfig.MCPServers, MCPServer{
			Name: remote.Name, Command: remote.Command, Args: remote.Args, Env: env,
		})
	}
	approver := &serverApprover{server: s, sessionID: id}
	writer := &sessionTextWriter{server: s, sessionID: id}
	runner, closeRuntime, err := s.Factory(ctx, sessionConfig, approver, writer, io.Discard)
	if err != nil {
		s.respondError(incoming.ID, -32000, err.Error())
		return
	}
	if closeRuntime == nil {
		closeRuntime = func() {}
	}
	runner.ToolObserver = &sessionToolObserver{server: s, sessionID: id}
	created := &session{id: id, runner: runner, close: closeRuntime}
	s.mu.Lock()
	s.sessions[id] = created
	s.mu.Unlock()
	s.respond(incoming.ID, map[string]any{"sessionId": id})
}

func (s *Server) handlePrompt(parent context.Context, incoming message) {
	var params struct {
		SessionID string `json:"sessionId"`
		Prompt    []struct {
			Type string `json:"type"`
			Text string `json:"text"`
			URI  string `json:"uri"`
			Name string `json:"name"`
		} `json:"prompt"`
	}
	if err := json.Unmarshal(incoming.Params, &params); err != nil {
		s.respondError(incoming.ID, -32602, "invalid prompt parameters")
		return
	}
	current := s.lookupSession(params.SessionID)
	if current == nil {
		s.respondError(incoming.ID, -32602, "unknown session")
		return
	}
	var parts []string
	for _, block := range params.Prompt {
		switch block.Type {
		case "text":
			parts = append(parts, block.Text)
		case "resource_link":
			parts = append(parts, fmt.Sprintf("Referenced resource %s: %s", block.Name, block.URI))
		}
	}
	prompt := strings.TrimSpace(strings.Join(parts, "\n\n"))
	if prompt == "" {
		s.respondError(incoming.ID, -32602, "prompt must contain text or resource links")
		return
	}
	current.mu.Lock()
	if current.running {
		current.mu.Unlock()
		s.respondError(incoming.ID, -32000, "session already has an active prompt")
		return
	}
	runCtx, cancel := context.WithCancel(parent)
	current.cancel = cancel
	current.running = true
	previous := current.previous
	current.mu.Unlock()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		result, err := current.runner.RunTurn(runCtx, prompt, previous)
		stopReason := "end_turn"
		if errors.Is(runCtx.Err(), context.Canceled) {
			stopReason = "cancelled"
		} else if err != nil {
			s.respondError(incoming.ID, -32000, err.Error())
		}
		current.mu.Lock()
		if err == nil {
			current.previous = result.ResponseID
		}
		current.running = false
		current.cancel = nil
		current.mu.Unlock()
		if err == nil || stopReason == "cancelled" {
			s.respond(incoming.ID, map[string]any{"stopReason": stopReason})
		}
	}()
}

func (s *Server) handleCancel(raw json.RawMessage) {
	var params struct {
		SessionID string `json:"sessionId"`
	}
	if json.Unmarshal(raw, &params) != nil {
		return
	}
	current := s.lookupSession(params.SessionID)
	if current == nil {
		return
	}
	current.mu.Lock()
	if current.cancel != nil {
		current.cancel()
	}
	current.mu.Unlock()
}

func (s *Server) handleClose(incoming message) {
	var params struct {
		SessionID string `json:"sessionId"`
	}
	if json.Unmarshal(incoming.Params, &params) != nil {
		s.respondError(incoming.ID, -32602, "invalid session close parameters")
		return
	}
	s.mu.Lock()
	current := s.sessions[params.SessionID]
	delete(s.sessions, params.SessionID)
	s.mu.Unlock()
	if current == nil {
		s.respondError(incoming.ID, -32602, "unknown session")
		return
	}
	current.mu.Lock()
	if current.cancel != nil {
		current.cancel()
	}
	current.mu.Unlock()
	current.close()
	s.respond(incoming.ID, map[string]any{})
}

func (s *Server) lookupSession(id string) *session {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[id]
}

func (s *Server) closeAll() {
	s.mu.Lock()
	sessions := make([]*session, 0, len(s.sessions))
	for _, current := range s.sessions {
		sessions = append(sessions, current)
	}
	s.sessions = make(map[string]*session)
	for _, pending := range s.pending {
		select {
		case pending <- permissionResult{err: io.EOF}:
		default:
		}
	}
	s.mu.Unlock()
	for _, current := range sessions {
		current.mu.Lock()
		if current.cancel != nil {
			current.cancel()
		}
		current.mu.Unlock()
		current.close()
	}
}

func (s *Server) respond(id json.RawMessage, result any) {
	s.write(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func (s *Server) respondError(id json.RawMessage, code int, message string) {
	s.write(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": message}})
}

func (s *Server) notify(sessionID string, update any) {
	s.write(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": sessionID, "update": update}})
}

func (s *Server) write(value any) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_ = json.NewEncoder(s.output).Encode(value)
}

type sessionTextWriter struct {
	server    *Server
	sessionID string
}

func (w *sessionTextWriter) Write(data []byte) (int, error) {
	if len(data) > 0 {
		w.server.notify(w.sessionID, map[string]any{
			"sessionUpdate": "agent_message_chunk",
			"content":       map[string]any{"type": "text", "text": string(data)},
		})
	}
	return len(data), nil
}

type sessionToolObserver struct {
	server    *Server
	sessionID string
}

func (o *sessionToolObserver) ToolStarted(call api.ToolCall) {
	o.server.notify(o.sessionID, map[string]any{
		"sessionUpdate": "tool_call", "toolCallId": call.CallID,
		"title": call.Name, "kind": acpToolKind(call.Name), "status": "in_progress",
		"rawInput": json.RawMessage(call.Arguments),
	})
}

func (o *sessionToolObserver) ToolFinished(call api.ToolCall, output string, toolErr error) {
	status := "completed"
	if toolErr != nil {
		status = "failed"
		if output == "" {
			output = toolErr.Error()
		}
	}
	o.server.notify(o.sessionID, map[string]any{
		"sessionUpdate": "tool_call_update", "toolCallId": call.CallID,
		"status": status, "rawOutput": output,
	})
}

func acpToolKind(name string) string {
	switch name {
	case "read_file", "list_dir", "list_files", "get_task_output", "get_background_command_output", "lsp":
		return "read"
	case "grep", "search_files":
		return "search"
	case "write_file", "edit_file", "search_replace":
		return "edit"
	case "run_terminal_cmd", "shell", "start_background_command", "kill_task", "kill_background_command":
		return "execute"
	case "todo_write", "update_goal":
		return "think"
	default:
		return "other"
	}
}

type serverApprover struct {
	server    *Server
	sessionID string
}

func (a *serverApprover) Approve(ctx context.Context, action, detail string) error {
	id := fmt.Sprintf("gork-permission-%d", a.server.nextRequest.Add(1))
	toolCallID := id
	if call, ok := tools.ToolCallFromContext(ctx); ok && call.ID != "" {
		toolCallID = call.ID
	}
	result := make(chan permissionResult, 1)
	a.server.mu.Lock()
	a.server.pending[id] = result
	a.server.mu.Unlock()
	defer func() { a.server.mu.Lock(); delete(a.server.pending, id); a.server.mu.Unlock() }()
	a.server.write(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": "session/request_permission",
		"params": map[string]any{
			"sessionId": a.sessionID,
			"toolCall":  map[string]any{"toolCallId": toolCallID, "title": action + ": " + detail, "status": "pending", "rawInput": detail},
			"options": []any{
				map[string]any{"optionId": "allow_once", "name": "Allow once", "kind": "allow_once"},
				map[string]any{"optionId": "reject_once", "name": "Reject", "kind": "reject_once"},
			},
		},
	})
	select {
	case response := <-result:
		if response.err != nil {
			return response.err
		}
		if response.outcome == "selected" && response.optionID == "allow_once" {
			return nil
		}
		return fmt.Errorf("permission denied for %s", action)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Server) handleClientResponse(incoming message) {
	key := strings.Trim(string(incoming.ID), "\"")
	s.mu.Lock()
	pending := s.pending[key]
	s.mu.Unlock()
	if pending == nil {
		return
	}
	if len(incoming.Error) > 0 && string(incoming.Error) != "null" {
		pending <- permissionResult{err: errors.New("ACP permission request failed")}
		return
	}
	var response struct {
		Outcome struct {
			Outcome  string `json:"outcome"`
			OptionID string `json:"optionId"`
		} `json:"outcome"`
	}
	if json.Unmarshal(incoming.Result, &response) != nil {
		pending <- permissionResult{err: errors.New("invalid ACP permission response")}
		return
	}
	pending <- permissionResult{outcome: response.Outcome.Outcome, optionID: response.Outcome.OptionID}
}
