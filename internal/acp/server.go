package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/api"
	sessionlog "github.com/lookcorner/go-cli/internal/session"
	"github.com/lookcorner/go-cli/internal/tools"
	worktrees "github.com/lookcorner/go-cli/internal/worktree"
)

const ProtocolVersion = 1

type SessionConfig struct {
	CWD        string
	MCPServers []MCPServer
	SessionID  string
	ResumePath string
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
	SessionDir  string
	input       io.Reader
	output      io.Writer
	writeMu     sync.Mutex
	mu          sync.Mutex
	sessions    map[string]*session
	pending     map[string]chan permissionResult
	nextSession atomic.Uint64
	nextRequest atomic.Uint64
	wg          sync.WaitGroup
	worktrees   *worktrees.Manager
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
	cwd      string
	title    string
	updated  time.Time
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

type promptBlock struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	URI      string `json:"uri"`
	Name     string `json:"name"`
	Resource struct {
		URI      string `json:"uri"`
		MimeType string `json:"mimeType"`
		Text     string `json:"text"`
		Blob     string `json:"blob"`
	} `json:"resource"`
}

func (s *Server) Serve(ctx context.Context, input io.Reader, output io.Writer) error {
	if s.Factory == nil {
		return errors.New("ACP session factory is required")
	}
	s.input, s.output = input, output
	s.sessions = make(map[string]*session)
	s.pending = make(map[string]chan permissionResult)
	manager, err := worktrees.NewManager(s.SessionDir)
	if err != nil {
		return err
	}
	s.worktrees = manager
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
					"loadSession":         true,
					"promptCapabilities":  map[string]any{"image": false, "audio": false, "embeddedContext": true},
					"mcpCapabilities":     map[string]any{"http": false, "sse": false},
					"sessionCapabilities": map[string]any{"close": map[string]any{}, "list": map[string]any{}, "resume": map[string]any{}},
					"auth":                map[string]any{},
				},
				"authMethods": []any{},
				"agentInfo":   map[string]any{"name": "gork-go", "version": "0.1.0-dev"},
			})
		case "session/new":
			s.handleNewSession(ctx, incoming)
		case "session/list":
			s.handleListSessions(incoming)
		case "session/load":
			s.handleRestoreSession(ctx, incoming, true)
		case "session/resume":
			s.handleRestoreSession(ctx, incoming, false)
		case "session/prompt":
			s.handlePrompt(ctx, incoming)
		case "session/cancel":
			s.handleCancel(incoming.Params)
		case "session/close":
			s.handleClose(incoming)
		case "x.ai/hunk-tracker/get-hunks", "x.ai/hunk-tracker/get-files", "x.ai/hunk-tracker/get-summary":
			s.handleHunkQuery(ctx, incoming)
		case "x.ai/hunk-tracker/hunk-action", "x.ai/hunk-tracker/file-action", "x.ai/hunk-tracker/all-action":
			s.handleHunkAction(ctx, incoming)
		case "x.ai/git/worktree/create", "x.ai/git/worktree/list", "x.ai/git/worktree/show", "x.ai/git/worktree/remove", "x.ai/git/worktree/apply":
			s.handleWorktree(ctx, incoming)
		case "x.ai/git/worktree/create_from_worktree", "x.ai/git/worktree/create_from_worktree_sync":
			s.handleWorktreeFork(ctx, incoming)
		case "x.ai/git/worktree/gc", "x.ai/git/worktree/db/stats", "x.ai/git/worktree/db/rebuild", "x.ai/git/worktree/db/path":
			s.handleWorktreeManagement(ctx, incoming)
		default:
			if len(incoming.ID) > 0 {
				s.respondError(incoming.ID, -32601, "method not found")
			}
		}
	}
}

func (s *Server) handleWorktreeManagement(ctx context.Context, incoming message) {
	switch incoming.Method {
	case "x.ai/git/worktree/gc":
		var req struct {
			DryRun bool   `json:"dryRun"`
			MaxAge string `json:"maxAge"`
			Force  bool   `json:"force"`
		}
		if json.Unmarshal(incoming.Params, &req) != nil {
			s.respondError(incoming.ID, -32602, "invalid worktree GC parameters")
			return
		}
		var maxAge *time.Duration
		if req.MaxAge != "" {
			value, err := time.ParseDuration(req.MaxAge)
			if err != nil {
				if strings.HasSuffix(req.MaxAge, "d") {
					days, dayErr := time.ParseDuration(strings.TrimSuffix(req.MaxAge, "d") + "h")
					if dayErr == nil {
						value, err = days*24, nil
					}
				}
				if err != nil {
					s.respondError(incoming.ID, -32602, "invalid maxAge; expected e.g. 7d, 24h, 30m, or 60s")
					return
				}
			}
			maxAge = &value
		}
		report, err := s.worktrees.GC(ctx, req.DryRun, maxAge, req.Force)
		if err != nil {
			s.respondError(incoming.ID, -32000, err.Error())
			return
		}
		s.respond(incoming.ID, report)
	case "x.ai/git/worktree/db/stats":
		s.respond(incoming.ID, s.worktrees.Stats())
	case "x.ai/git/worktree/db/rebuild":
		report, err := s.worktrees.Rebuild(ctx)
		if err != nil {
			s.respondError(incoming.ID, -32000, err.Error())
			return
		}
		s.respond(incoming.ID, report)
	case "x.ai/git/worktree/db/path":
		s.respond(incoming.ID, map[string]any{"path": s.worktrees.StatePath()})
	}
}

func (s *Server) handleWorktreeFork(ctx context.Context, incoming message) {
	var req worktrees.ForkRequest
	if json.Unmarshal(incoming.Params, &req) != nil {
		s.respondError(incoming.ID, -32602, "invalid create-from-worktree parameters")
		return
	}
	result, existed, err := s.worktrees.CreateFromWorktree(ctx, req)
	if err != nil {
		s.respondError(incoming.ID, -32000, err.Error())
		return
	}
	if incoming.Method == "x.ai/git/worktree/create_from_worktree_sync" {
		s.respond(incoming.ID, result)
		return
	}
	status := "creating"
	if existed {
		status = "exists"
	}
	s.respond(incoming.ID, map[string]any{
		"status": status, "sessionId": req.NewSessionID, "worktreePath": result.WorktreePath,
		"sourceGitRoot": result.SourceGitRoot,
	})
	if !existed {
		s.write(map[string]any{
			"jsonrpc": "2.0", "method": "x.ai/git/worktree/status",
			"params": map[string]any{
				"status": "created", "sessionId": req.NewSessionID, "worktreePath": result.WorktreePath,
				"commit": *result.Commit, "sourceGitRoot": result.SourceGitRoot,
			},
		})
	}
}

func (s *Server) handleWorktree(ctx context.Context, incoming message) {
	if s.worktrees == nil {
		s.respondError(incoming.ID, -32000, "worktree manager unavailable")
		return
	}
	switch incoming.Method {
	case "x.ai/git/worktree/create":
		var req worktrees.CreateRequest
		if json.Unmarshal(incoming.Params, &req) != nil {
			s.respondError(incoming.ID, -32602, "invalid worktree create parameters")
			return
		}
		record, existed, err := s.worktrees.Create(ctx, req)
		if err != nil {
			s.respondError(incoming.ID, -32000, err.Error())
			return
		}
		status := "creating"
		if existed {
			status = "exists"
		}
		s.respond(incoming.ID, map[string]any{
			"status": status, "sessionId": req.SessionID, "worktreePath": record.Path,
			"sourceGitRoot": record.SourceRepo,
		})
		if !existed {
			s.write(map[string]any{
				"jsonrpc": "2.0", "method": "x.ai/git/worktree/status",
				"params": map[string]any{
					"status": "created", "sessionId": req.SessionID, "worktreePath": record.Path,
					"commit": record.HeadCommit, "sourceGitRoot": record.SourceRepo,
				},
			})
		}
	case "x.ai/git/worktree/list":
		var req struct {
			Repo       string   `json:"repo"`
			Types      []string `json:"type"`
			IncludeAll bool     `json:"includeAll"`
		}
		if json.Unmarshal(incoming.Params, &req) != nil {
			s.respondError(incoming.ID, -32602, "invalid worktree list parameters")
			return
		}
		s.respond(incoming.ID, s.worktrees.List(req.Repo, req.Types, req.IncludeAll))
	case "x.ai/git/worktree/show":
		var req struct {
			IDOrPath string `json:"idOrPath"`
		}
		if json.Unmarshal(incoming.Params, &req) != nil || req.IDOrPath == "" {
			s.respondError(incoming.ID, -32602, "idOrPath is required")
			return
		}
		record, ok := s.worktrees.Show(req.IDOrPath)
		if !ok {
			s.respond(incoming.ID, nil)
			return
		}
		s.respond(incoming.ID, record)
	case "x.ai/git/worktree/remove":
		var req worktrees.RemoveRequest
		if json.Unmarshal(incoming.Params, &req) != nil {
			s.respondError(incoming.ID, -32602, "invalid worktree remove parameters")
			return
		}
		removed, path, err := s.worktrees.Remove(ctx, req)
		if err != nil {
			s.respondError(incoming.ID, -32000, err.Error())
			return
		}
		s.respond(incoming.ID, map[string]any{"removed": removed, "resolvedPath": path})
	case "x.ai/git/worktree/apply":
		var req worktrees.ApplyRequest
		if json.Unmarshal(incoming.Params, &req) != nil {
			s.respondError(incoming.ID, -32602, "invalid worktree apply parameters")
			return
		}
		result, err := s.worktrees.Apply(ctx, req)
		if err != nil {
			s.respondError(incoming.ID, -32000, err.Error())
			return
		}
		s.respond(incoming.ID, result)
	}
}

func (s *Server) handleHunkAction(ctx context.Context, incoming message) {
	var params struct {
		SessionID string `json:"sessionId"`
		HunkID    string `json:"hunkId"`
		Path      string `json:"path"`
		Action    string `json:"action"`
	}
	if json.Unmarshal(incoming.Params, &params) != nil || params.SessionID == "" {
		s.respondError(incoming.ID, -32602, "sessionId is required")
		return
	}
	if params.Action != "accept" && params.Action != "reject" {
		s.respondError(incoming.ID, -32602, "action must be accept or reject")
		return
	}
	current := s.lookupSession(params.SessionID)
	if current == nil || current.runner == nil || current.runner.Tools == nil {
		s.respondError(incoming.ID, -32602, "unknown session")
		return
	}
	tracker := current.runner.Tools.HunkTracker()
	var count int
	var err error
	switch incoming.Method {
	case "x.ai/hunk-tracker/hunk-action":
		count, err = tracker.HunkAction(ctx, params.HunkID, params.Action)
	case "x.ai/hunk-tracker/file-action":
		count, err = tracker.FileAction(ctx, params.Path, params.Action)
	case "x.ai/hunk-tracker/all-action":
		count, err = tracker.AllAction(ctx, params.Action)
	}
	if err != nil {
		s.respond(incoming.ID, map[string]any{"success": false, "error": err.Error(), "affectedCount": 0})
		return
	}
	s.respond(incoming.ID, map[string]any{"success": true, "affectedCount": count})
}

func (s *Server) handleHunkQuery(ctx context.Context, incoming message) {
	var params struct {
		SessionID string `json:"sessionId"`
		Path      string `json:"path"`
		Source    string `json:"source"`
	}
	if json.Unmarshal(incoming.Params, &params) != nil || params.SessionID == "" {
		s.respondError(incoming.ID, -32602, "sessionId is required")
		return
	}
	if params.Source != "" && params.Source != "all" && params.Source != "agent" && params.Source != "external" {
		s.respondError(incoming.ID, -32602, "source must be agent, external, or all")
		return
	}
	current := s.lookupSession(params.SessionID)
	if current == nil || current.runner == nil || current.runner.Tools == nil {
		s.respondError(incoming.ID, -32602, "unknown session")
		return
	}
	tracker := current.runner.Tools.HunkTracker()
	switch incoming.Method {
	case "x.ai/hunk-tracker/get-hunks":
		hunks, err := tracker.Hunks(ctx, params.Path, params.Source)
		if err != nil {
			s.respondError(incoming.ID, -32000, err.Error())
			return
		}
		s.respond(incoming.ID, map[string]any{"hunks": hunks})
	case "x.ai/hunk-tracker/get-files":
		files, err := tracker.Files(ctx)
		if err != nil {
			s.respondError(incoming.ID, -32000, err.Error())
			return
		}
		s.respond(incoming.ID, map[string]any{"files": files})
	case "x.ai/hunk-tracker/get-summary":
		files, err := tracker.Files(ctx)
		if err != nil {
			s.respondError(incoming.ID, -32000, err.Error())
			return
		}
		var hunks, additions, deletions, agentFiles int
		for _, file := range files {
			hunks += file.HunkCount
			additions += file.Additions
			deletions += file.Deletions
			if file.IsAgentFile {
				agentFiles++
			}
		}
		s.respond(incoming.ID, map[string]any{
			"fileCount": len(files), "hunkCount": hunks, "agentFileCount": agentFiles,
			"additions": additions, "deletions": deletions,
		})
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
	id := fmt.Sprintf("gork-%s-%d", time.Now().UTC().Format("20060102T150405.000000000Z"), s.nextSession.Add(1))
	sessionConfig := SessionConfig{CWD: params.CWD, SessionID: id, MCPServers: make([]MCPServer, 0, len(params.MCPServers))}
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
	_, err := s.startSession(ctx, id, sessionConfig, "")
	if err != nil {
		s.respondError(incoming.ID, -32000, err.Error())
		return
	}
	s.respond(incoming.ID, map[string]any{"sessionId": id})
}

func (s *Server) startSession(ctx context.Context, id string, sessionConfig SessionConfig, previous string) (*session, error) {
	approver := &serverApprover{server: s, sessionID: id}
	writer := &sessionTextWriter{server: s, sessionID: id}
	runner, closeRuntime, err := s.Factory(ctx, sessionConfig, approver, writer, io.Discard)
	if err != nil {
		return nil, err
	}
	if closeRuntime == nil {
		closeRuntime = func() {}
	}
	runner.ToolObserver = &sessionToolObserver{server: s, sessionID: id}
	created := &session{
		id: id, cwd: sessionConfig.CWD, updated: time.Now().UTC(), previous: previous,
		runner: runner, close: closeRuntime,
	}
	s.mu.Lock()
	if _, exists := s.sessions[id]; exists {
		s.mu.Unlock()
		closeRuntime()
		return nil, errors.New("session is already active")
	}
	s.sessions[id] = created
	s.mu.Unlock()
	return created, nil
}

func (s *Server) handlePrompt(parent context.Context, incoming message) {
	var params struct {
		SessionID string        `json:"sessionId"`
		Prompt    []promptBlock `json:"prompt"`
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
	parts, err := renderPrompt(params.Prompt)
	if err != nil {
		s.respondError(incoming.ID, -32602, err.Error())
		return
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
	if current.title == "" {
		current.title = promptTitle(prompt)
		s.notify(current.id, map[string]any{
			"sessionUpdate": "session_info_update", "title": current.title,
			"updatedAt": time.Now().UTC().Format(time.RFC3339),
		})
	}
	current.updated = time.Now().UTC()
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
		current.updated = time.Now().UTC()
		current.mu.Unlock()
		if err == nil || stopReason == "cancelled" {
			s.respond(incoming.ID, map[string]any{"stopReason": stopReason})
		}
	}()
}

func (s *Server) handleRestoreSession(ctx context.Context, incoming message, replay bool) {
	var params struct {
		SessionID  string `json:"sessionId"`
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
	if json.Unmarshal(incoming.Params, &params) != nil || params.SessionID == "" || params.CWD == "" {
		s.respondError(incoming.ID, -32602, "sessionId and cwd are required")
		return
	}
	path, err := sessionlog.PathForID(s.SessionDir, params.SessionID)
	if err != nil {
		s.respondError(incoming.ID, -32602, err.Error())
		return
	}
	items, err := sessionlog.List(s.SessionDir, "")
	if err != nil {
		s.respondError(incoming.ID, -32000, err.Error())
		return
	}
	found := false
	for _, item := range items {
		if item.SessionID == params.SessionID {
			found = true
			if item.CWD != params.CWD {
				s.respondError(incoming.ID, -32602, "cwd does not match the stored session")
				return
			}
			break
		}
	}
	if !found {
		s.respondError(incoming.ID, -32602, "unknown persisted session")
		return
	}
	previous, err := sessionlog.CompletedResponseID(path)
	if err != nil {
		s.respondError(incoming.ID, -32000, err.Error())
		return
	}
	config := SessionConfig{
		CWD: params.CWD, SessionID: params.SessionID, ResumePath: path,
		MCPServers: make([]MCPServer, 0, len(params.MCPServers)),
	}
	for _, remote := range params.MCPServers {
		if remote.Name == "" || remote.Command == "" {
			s.respondError(incoming.ID, -32602, "stdio MCP servers require name and command")
			return
		}
		env := make(map[string]string, len(remote.Env))
		for _, entry := range remote.Env {
			env[entry.Name] = entry.Value
		}
		config.MCPServers = append(config.MCPServers, MCPServer{
			Name: remote.Name, Command: remote.Command, Args: remote.Args, Env: env,
		})
	}
	if _, err := s.startSession(ctx, params.SessionID, config, previous); err != nil {
		s.respondError(incoming.ID, -32000, err.Error())
		return
	}
	if replay {
		messages, err := sessionlog.Transcript(path)
		if err != nil {
			s.closeSession(params.SessionID)
			s.respondError(incoming.ID, -32000, err.Error())
			return
		}
		for _, historical := range messages {
			updateType := "agent_message_chunk"
			if historical.Role == "user" {
				updateType = "user_message_chunk"
			}
			s.notify(params.SessionID, map[string]any{
				"sessionUpdate": updateType,
				"content":       map[string]any{"type": "text", "text": historical.Text},
			})
		}
	}
	s.respond(incoming.ID, map[string]any{"sessionId": params.SessionID})
}

func (s *Server) closeSession(id string) {
	s.mu.Lock()
	current := s.sessions[id]
	delete(s.sessions, id)
	s.mu.Unlock()
	if current != nil {
		current.close()
	}
}

func (s *Server) handleListSessions(incoming message) {
	var params struct {
		CWD    string `json:"cwd"`
		Cursor string `json:"cursor"`
	}
	if len(incoming.Params) > 0 && string(incoming.Params) != "null" {
		if err := json.Unmarshal(incoming.Params, &params); err != nil {
			s.respondError(incoming.ID, -32602, "invalid session list parameters")
			return
		}
	}
	if params.Cursor != "" {
		s.respondError(incoming.ID, -32602, "invalid session list cursor")
		return
	}
	type info struct {
		SessionID string `json:"sessionId"`
		CWD       string `json:"cwd"`
		Title     string `json:"title,omitempty"`
		UpdatedAt string `json:"updatedAt,omitempty"`
	}
	persisted, err := sessionlog.List(s.SessionDir, params.CWD)
	if err != nil {
		s.respondError(incoming.ID, -32000, err.Error())
		return
	}
	byID := make(map[string]info, len(persisted))
	for _, item := range persisted {
		byID[item.SessionID] = info{
			SessionID: item.SessionID, CWD: item.CWD, Title: item.Title,
			UpdatedAt: item.UpdatedAt.Format(time.RFC3339),
		}
	}
	s.mu.Lock()
	for _, current := range s.sessions {
		current.mu.Lock()
		if params.CWD == "" || current.cwd == params.CWD {
			byID[current.id] = info{
				SessionID: current.id, CWD: current.cwd, Title: current.title,
				UpdatedAt: current.updated.Format(time.RFC3339),
			}
		}
		current.mu.Unlock()
	}
	s.mu.Unlock()
	items := make([]info, 0, len(byID))
	for _, item := range byID {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].UpdatedAt == items[j].UpdatedAt {
			return items[i].SessionID < items[j].SessionID
		}
		return items[i].UpdatedAt > items[j].UpdatedAt
	})
	s.respond(incoming.ID, map[string]any{"sessions": items})
}

func promptTitle(prompt string) string {
	line := strings.TrimSpace(strings.SplitN(prompt, "\n", 2)[0])
	runes := []rune(line)
	if len(runes) > 80 {
		return string(runes[:79]) + "…"
	}
	return line
}

func renderPrompt(blocks []promptBlock) ([]string, error) {
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		switch block.Type {
		case "text":
			if block.Text != "" {
				parts = append(parts, block.Text)
			}
		case "resource_link":
			if block.Name == "" || block.URI == "" {
				return nil, errors.New("resource links require name and uri")
			}
			parts = append(parts, fmt.Sprintf("Referenced resource %s: %s", block.Name, block.URI))
		case "resource":
			if block.Resource.URI == "" {
				return nil, errors.New("embedded resources require a uri")
			}
			if block.Resource.Text == "" {
				if block.Resource.Blob != "" {
					return nil, errors.New("binary embedded resources are not supported")
				}
				return nil, errors.New("embedded text resources require text")
			}
			header := "Embedded resource " + block.Resource.URI
			if block.Resource.MimeType != "" {
				header += " (" + block.Resource.MimeType + ")"
			}
			parts = append(parts, header+":\n"+block.Resource.Text)
		case "image", "audio":
			return nil, fmt.Errorf("%s prompt content is not supported", block.Type)
		default:
			return nil, fmt.Errorf("unsupported prompt content type %q", block.Type)
		}
	}
	return parts, nil
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
