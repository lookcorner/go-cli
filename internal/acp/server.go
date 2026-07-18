package acp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/api"
	sessionlog "github.com/lookcorner/go-cli/internal/session"
	"github.com/lookcorner/go-cli/internal/tools"
	"github.com/lookcorner/go-cli/internal/workspace"
	worktrees "github.com/lookcorner/go-cli/internal/worktree"
)

const ProtocolVersion = 1

type SessionConfig struct {
	CWD        string
	Model      string
	MCPServers []MCPServer
	SessionID  string
	ResumePath string
}

type MCPServer struct {
	Type    string
	Name    string
	Command string
	Args    []string
	Env     map[string]string
	URL     string
	Headers map[string]string
}

type mcpServerParam struct {
	Type    string   `json:"type"`
	Name    string   `json:"name"`
	Command string   `json:"command"`
	Args    []string `json:"args"`
	Env     []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	} `json:"env"`
	URL     string `json:"url"`
	Headers []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	} `json:"headers"`
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
	terminals   *terminalManager
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
	id           string
	cwd          string
	title        string
	updated      time.Time
	runner       *agent.Runner
	close        func()
	mu           sync.Mutex
	previous     string
	cancel       context.CancelFunc
	running      bool
	promptIndex  int
	activePrompt int
	rewind       *workspace.RewindStore
	logPath      string
	mode         string
}

type permissionResult struct {
	outcome  string
	optionID string
	err      error
}

type promptBlock struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	Data     string `json:"data"`
	MimeType string `json:"mimeType"`
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
	s.terminals = newTerminalManager(func(method string, params any) {
		s.write(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
	})
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
					"promptCapabilities":  map[string]any{"image": true, "audio": false, "embeddedContext": true},
					"mcpCapabilities":     map[string]any{"http": true, "sse": true},
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
		case "session/set_mode":
			s.handleSetMode(incoming)
		case "session/cancel":
			s.handleCancel(incoming.Params)
		case "session/close":
			s.handleClose(incoming)
		case "x.ai/hunk-tracker/get-hunks", "x.ai/hunk-tracker/get-files", "x.ai/hunk-tracker/get-all-file-contents", "x.ai/hunk-tracker/get-summary":
			s.handleHunkQuery(ctx, incoming)
		case "x.ai/hunk-tracker/hunk-action", "x.ai/hunk-tracker/file-action", "x.ai/hunk-tracker/turn-action", "x.ai/hunk-tracker/all-action":
			s.handleHunkAction(ctx, incoming)
		case "x.ai/git/worktree/create", "x.ai/git/worktree/list", "x.ai/git/worktree/show", "x.ai/git/worktree/remove", "x.ai/git/worktree/apply":
			s.handleWorktree(ctx, incoming)
		case "x.ai/git/worktree/create_from_worktree", "x.ai/git/worktree/create_from_worktree_sync":
			s.handleWorktreeFork(ctx, incoming)
		case "x.ai/git/worktree/gc", "x.ai/git/worktree/db/stats", "x.ai/git/worktree/db/rebuild", "x.ai/git/worktree/db/path":
			s.handleWorktreeManagement(ctx, incoming)
		case "x.ai/git/worktree/resume_session":
			s.handleResumeSessionInWorktree(ctx, incoming)
		case "x.ai/session/rehydrate":
			s.handleRehydrateSession(ctx, incoming)
		case "x.ai/session/resolve_local_for_worktree_resume":
			s.handleResolveLocalSession(ctx, incoming)
		case "x.ai/session/fork":
			s.handleSessionFork(incoming)
		case "x.ai/rewind/points", "x.ai/rewind/execute":
			s.handleRewind(incoming)
		case "x.ai/terminal/pty/create", "x.ai/terminal/pty/load", "x.ai/terminal/pty/resize", "x.ai/terminal/list", "x.ai/terminal/kill":
			s.handleTerminal(incoming)
		case "x.ai/terminal/pty/input":
			s.handleTerminalInput(incoming.Params)
		default:
			if len(incoming.ID) > 0 {
				s.respondError(incoming.ID, -32601, "method not found")
			}
		}
	}
}

func (s *Server) handleResolveLocalSession(ctx context.Context, incoming message) {
	var req struct {
		SessionID string `json:"sessionId"`
		CWD       string `json:"cwd"`
	}
	if json.Unmarshal(incoming.Params, &req) != nil || req.SessionID == "" || req.CWD == "" {
		s.respondError(incoming.ID, -32602, "sessionId and cwd are required")
		return
	}
	items, err := sessionlog.List(s.SessionDir, "")
	if err != nil {
		s.respondError(incoming.ID, -32000, err.Error())
		return
	}
	for _, item := range items {
		if item.SessionID != req.SessionID {
			continue
		}
		kind := "exactCwd"
		requested, requestErr := filepath.EvalSymlinks(req.CWD)
		stored, storedErr := filepath.EvalSymlinks(item.CWD)
		if requestErr != nil || storedErr != nil || requested != stored {
			requestRoot, requestRootErr := worktrees.MainRoot(ctx, req.CWD)
			storedRoot, storedRootErr := worktrees.MainRoot(ctx, item.CWD)
			if requestRootErr != nil || storedRootErr != nil || requestRoot != storedRoot {
				break
			}
			kind = "sameRepoDifferentCwd"
		}
		s.respond(incoming.ID, map[string]any{
			"found": true, "resolvedSessionId": item.SessionID, "resolvedCwd": item.CWD, "resolutionKind": kind,
		})
		return
	}
	s.respond(incoming.ID, map[string]any{"found": false})
}

func (s *Server) handleResumeSessionInWorktree(ctx context.Context, incoming message) {
	var req struct {
		SessionID    string `json:"sessionId"`
		SourceCWD    string `json:"sourceCwd"`
		CopyMode     string `json:"copyMode"`
		WorktreeType string `json:"worktreeType"`
		RestoreCode  *bool  `json:"restoreCode"`
		GitRef       string `json:"gitRef"`
	}
	if json.Unmarshal(incoming.Params, &req) != nil || req.SessionID == "" || req.SourceCWD == "" {
		s.respondError(incoming.ID, -32602, "sessionId and sourceCwd are required")
		return
	}
	items, err := sessionlog.List(s.SessionDir, "")
	if err != nil {
		s.respondError(incoming.ID, -32000, err.Error())
		return
	}
	var persisted *sessionlog.Info
	for _, item := range items {
		if item.SessionID == req.SessionID {
			copy := item
			persisted = &copy
			break
		}
	}
	if persisted == nil {
		s.respondError(incoming.ID, -32000, "session not found locally and remote session registry is unavailable")
		return
	}
	newID := fmt.Sprintf("gork-%s-%d", time.Now().UTC().Format("20060102T150405.000000000Z"), s.nextSession.Add(1))
	fork, _, err := s.worktrees.CreateFromWorktree(ctx, worktrees.ForkRequest{
		SourceWorktreePath: req.SourceCWD, NewSessionID: newID, CopyMode: req.CopyMode,
		GitRef: req.GitRef, WorktreeType: req.WorktreeType, Label: "resume-" + req.SessionID,
	})
	if err != nil {
		s.respondError(incoming.ID, -32000, err.Error())
		return
	}
	effective, err := worktrees.EffectiveCWD(ctx, req.SourceCWD, fork.WorktreePath)
	if err != nil {
		_, _, _ = s.worktrees.Remove(ctx, worktrees.RemoveRequest{WorktreePath: fork.WorktreePath, Force: true})
		s.respondError(incoming.ID, -32000, err.Error())
		return
	}
	codeRestored := false
	restoreSummary, restoreDegree := "", ""
	if req.RestoreCode != nil && *req.RestoreCode {
		if persisted.HeadCommit == "" {
			restoreSummary = "restore skipped (session HEAD unavailable)"
		} else {
			outcome := worktrees.RestoreCommit(ctx, fork.WorktreePath, persisted.HeadCommit, req.SessionID)
			codeRestored, restoreSummary, restoreDegree = worktrees.RestoreSummary(persisted.HeadCommit, outcome)
		}
	}
	chat, updates, err := sessionlog.Fork(s.SessionDir, req.SessionID, newID, effective, "", nil)
	if err != nil {
		_, _, _ = s.worktrees.Remove(ctx, worktrees.RemoveRequest{WorktreePath: fork.WorktreePath, Force: true})
		s.respondError(incoming.ID, -32000, err.Error())
		return
	}
	response := map[string]any{
		"sessionId": newID, "worktreePath": fork.WorktreePath, "effectiveCwd": effective,
		"remoteRestored": false, "parentSessionId": req.SessionID,
		"chatMessagesCopied": chat, "updatesCopied": updates, "codeRestored": codeRestored,
	}
	if restoreSummary != "" {
		response["restoreSummary"] = restoreSummary
	}
	if restoreDegree != "" {
		response["restoreDegree"] = restoreDegree
	}
	s.respond(incoming.ID, response)
}

func (s *Server) handleSessionFork(incoming message) {
	var req struct {
		SourceSessionID   string `json:"sourceSessionId"`
		SourceCWD         string `json:"sourceCwd"`
		NewCWD            string `json:"newCwd"`
		NewSessionID      string `json:"newSessionId"`
		NewModelID        string `json:"newModelId"`
		TargetPromptIndex *int   `json:"targetPromptIndex"`
	}
	if json.Unmarshal(incoming.Params, &req) != nil || req.SourceSessionID == "" || req.SourceCWD == "" || req.NewCWD == "" {
		s.respondError(incoming.ID, -32602, "sourceSessionId, sourceCwd, and newCwd are required")
		return
	}
	items, err := sessionlog.List(s.SessionDir, "")
	if err != nil {
		s.respondError(incoming.ID, -32000, err.Error())
		return
	}
	found := false
	for _, item := range items {
		if item.SessionID == req.SourceSessionID && item.CWD == req.SourceCWD {
			found = true
			break
		}
	}
	if !found {
		s.respondError(incoming.ID, -32602, "source session and cwd do not match a persisted session")
		return
	}
	if current := s.lookupSession(req.SourceSessionID); current != nil {
		current.mu.Lock()
		running := current.running
		current.mu.Unlock()
		if running {
			s.respondError(incoming.ID, -32000, "cannot fork while a prompt is running")
			return
		}
	}
	newID := req.NewSessionID
	if newID == "" {
		newID = fmt.Sprintf("gork-%s-%d", time.Now().UTC().Format("20060102T150405.000000000Z"), s.nextSession.Add(1))
	}
	chat, updates, err := sessionlog.Fork(s.SessionDir, req.SourceSessionID, newID, req.NewCWD, req.NewModelID, req.TargetPromptIndex)
	if err != nil {
		s.respondError(incoming.ID, -32000, err.Error())
		return
	}
	response := map[string]any{
		"newSessionId": newID, "chatMessagesCopied": chat, "updatesCopied": updates,
		"planStateCopied": false, "newCwd": req.NewCWD, "parentSessionId": req.SourceSessionID,
	}
	if req.NewModelID != "" {
		response["newModelId"] = req.NewModelID
	}
	s.respond(incoming.ID, response)
}

func (s *Server) handleRehydrateSession(ctx context.Context, incoming message) {
	var req struct {
		SessionID    string `json:"sessionId"`
		SourceCWD    string `json:"sourceCwd"`
		RepoRoot     string `json:"repoRoot"`
		WorktreePath string `json:"worktreePath"`
	}
	if json.Unmarshal(incoming.Params, &req) != nil || req.SessionID == "" || req.SourceCWD == "" || req.RepoRoot == "" {
		s.respondError(incoming.ID, -32602, "sessionId, sourceCwd, and repoRoot are required")
		return
	}
	path, err := sessionlog.PathForID(s.SessionDir, req.SessionID)
	if err != nil {
		s.respondError(incoming.ID, -32602, err.Error())
		return
	}
	if _, err := os.Stat(path); err != nil {
		s.respondError(incoming.ID, -32000, "local session state is unavailable and remote registry restore is unsupported")
		return
	}
	worktreePath := req.WorktreePath
	if worktreePath == "" {
		worktreePath = req.SourceCWD
	}
	created := false
	if _, err := os.Stat(worktreePath); os.IsNotExist(err) {
		_, _, createErr := s.worktrees.Create(ctx, worktrees.CreateRequest{
			SessionID: req.SessionID, SourcePath: req.RepoRoot, WorktreePath: worktreePath,
			CopyMode: "clean", WorktreeType: "linked", Label: filepath.Base(worktreePath),
		})
		if createErr != nil {
			s.respondError(incoming.ID, -32000, createErr.Error())
			return
		}
		created = true
	}
	s.respond(incoming.ID, map[string]any{
		"sessionId": req.SessionID, "worktreePath": worktreePath, "effectiveCwd": req.SourceCWD,
		"codebaseRestored": created, "sessionStateRestored": false, "memoryRestored": false,
		"warnings": []string{},
	})
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
		s.respond(incoming.ID, worktreeGCToWire(report))
	case "x.ai/git/worktree/db/stats":
		s.respond(incoming.ID, worktreeStatsToWire(s.worktrees.Stats()))
	case "x.ai/git/worktree/db/rebuild":
		report, err := s.worktrees.Rebuild(ctx)
		if err != nil {
			s.respondError(incoming.ID, -32000, err.Error())
			return
		}
		s.respond(incoming.ID, worktreeRebuildToWire(report))
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
	response := map[string]any{
		"status": status, "sessionId": req.NewSessionID, "worktreePath": result.WorktreePath,
		"sourceGitRoot": result.SourceGitRoot,
	}
	if existed && result.Commit != nil {
		response["commit"] = *result.Commit
	}
	s.respond(incoming.ID, response)
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
		response := map[string]any{
			"status": status, "sessionId": req.SessionID, "worktreePath": record.Path,
			"sourceGitRoot": record.SourceRepo,
		}
		if existed {
			response["commit"] = record.HeadCommit
		}
		s.respond(incoming.ID, response)
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
		s.respond(incoming.ID, worktreeRecordWires(s.worktrees.List(req.Repo, req.Types, req.IncludeAll)))
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
		s.respond(incoming.ID, worktreeRecordToWire(record))
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
		SessionID   string `json:"sessionId"`
		HunkID      string `json:"hunkId"`
		Path        string `json:"path"`
		PromptIndex *int   `json:"promptIndex"`
		Action      string `json:"action"`
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
	case "x.ai/hunk-tracker/turn-action":
		if params.PromptIndex == nil {
			s.respondError(incoming.ID, -32602, "promptIndex is required")
			return
		}
		count, err = tracker.TurnAction(ctx, *params.PromptIndex, params.Action)
	case "x.ai/hunk-tracker/all-action":
		count, err = tracker.AllAction(ctx, params.Action)
	}
	if err != nil {
		s.respond(incoming.ID, map[string]any{"success": false, "error": err.Error()})
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
	if params.Source != "agent" && params.Source != "external" {
		params.Source = "all"
	}
	current := s.lookupSession(params.SessionID)
	if current == nil || current.runner == nil || current.runner.Tools == nil {
		s.respondError(incoming.ID, -32602, "unknown session")
		return
	}
	tracker := current.runner.Tools.HunkTracker()
	switch incoming.Method {
	case "x.ai/hunk-tracker/get-hunks":
		if params.Path != "" {
			data, err := tracker.FileData(ctx, params.Path, params.Source)
			if err != nil {
				s.respondError(incoming.ID, -32000, err.Error())
				return
			}
			s.respond(incoming.ID, getHunksWire{
				Hunks:    hunkWires(data.Hunks, tracker, current.cwd, true),
				Baseline: &data.Baseline, Current: &data.Current,
				BaselineContent: data.BaselineContent, CurrentContent: data.CurrentContent,
			})
			return
		}
		hunks, err := tracker.Hunks(ctx, params.Path, params.Source)
		if err != nil {
			s.respondError(incoming.ID, -32000, err.Error())
			return
		}
		s.respond(incoming.ID, getHunksWire{Hunks: hunkWires(hunks, tracker, current.cwd, false)})
	case "x.ai/hunk-tracker/get-files":
		files, err := tracker.Files(ctx)
		if err != nil {
			s.respondError(incoming.ID, -32000, err.Error())
			return
		}
		for index := range files {
			files[index].Path = displayHunkPath(current.cwd, files[index].Path)
		}
		s.respond(incoming.ID, map[string]any{"files": files})
	case "x.ai/hunk-tracker/get-all-file-contents":
		files, err := tracker.AllFileContents(ctx)
		if err != nil {
			s.respondError(incoming.ID, -32000, err.Error())
			return
		}
		for index := range files {
			files[index].Path = displayHunkPath(current.cwd, files[index].Path)
		}
		s.respond(incoming.ID, map[string]any{"files": files})
	case "x.ai/hunk-tracker/get-summary":
		summary, err := tracker.Summary(ctx)
		if err != nil {
			s.respondError(incoming.ID, -32000, err.Error())
			return
		}
		s.respond(incoming.ID, hunkSummaryWire{
			Stats: summary.Stats, Turns: hunkTurnWires(summary.Turns, tracker, current.cwd),
			FilesModified: summary.FilesModified, FilesWithPending: summary.FilesWithPending,
			PendingHunks: summary.PendingHunks, PendingLinesAdded: summary.PendingLinesAdded,
			PendingLinesRemoved: summary.PendingLinesRemoved, UnattributedPending: summary.UnattributedPending,
		})
	}
}

func (s *Server) handleNewSession(ctx context.Context, incoming message) {
	var params struct {
		CWD        string           `json:"cwd"`
		MCPServers []mcpServerParam `json:"mcpServers"`
	}
	if json.Unmarshal(incoming.Params, &params) != nil || params.CWD == "" {
		s.respondError(incoming.ID, -32602, "cwd is required")
		return
	}
	id := fmt.Sprintf("gork-%s-%d", time.Now().UTC().Format("20060102T150405.000000000Z"), s.nextSession.Add(1))
	servers, err := parseMCPServers(params.MCPServers)
	if err != nil {
		s.respondError(incoming.ID, -32602, err.Error())
		return
	}
	sessionConfig := SessionConfig{CWD: params.CWD, SessionID: id, MCPServers: servers}
	_, err = s.startSession(ctx, id, sessionConfig, "")
	if err != nil {
		s.respondError(incoming.ID, -32000, err.Error())
		return
	}
	s.respond(incoming.ID, map[string]any{"sessionId": id, "modes": sessionModes("default")})
}

func (s *Server) handleRewind(incoming message) {
	var base struct {
		SessionID      string `json:"sessionId"`
		SessionIDSnake string `json:"session_id"`
	}
	if json.Unmarshal(incoming.Params, &base) != nil {
		s.respondError(incoming.ID, -32602, "invalid rewind parameters")
		return
	}
	sessionID := base.SessionID
	if sessionID == "" {
		sessionID = base.SessionIDSnake
	}
	current := s.lookupSession(sessionID)
	if current == nil {
		s.respondError(incoming.ID, -32602, "session not found")
		return
	}
	path, err := sessionlog.PathForID(s.SessionDir, sessionID)
	if err != nil {
		s.respondError(incoming.ID, -32602, err.Error())
		return
	}
	if incoming.Method == "x.ai/rewind/points" {
		current.mu.Lock()
		running := current.running
		current.mu.Unlock()
		if running {
			s.respondError(incoming.ID, -32000, "cannot list rewind points while a prompt is running")
			return
		}
		points, err := sessionlog.RewindPoints(path)
		if err != nil {
			s.respondError(incoming.ID, -32000, err.Error())
			return
		}
		counts, err := current.rewind.Counts()
		if err != nil {
			s.respondError(incoming.ID, -32000, err.Error())
			return
		}
		for index := range points {
			points[index].NumFileSnapshots = counts[points[index].PromptIndex]
			points[index].HasFileChanges = points[index].NumFileSnapshots > 0
		}
		s.respond(incoming.ID, map[string]any{"rewind_points": points})
		return
	}
	var req struct {
		Target      *int   `json:"targetPromptIndex"`
		TargetSnake *int   `json:"target_prompt_index"`
		Force       bool   `json:"force"`
		Mode        string `json:"mode"`
	}
	if json.Unmarshal(incoming.Params, &req) != nil {
		s.respondError(incoming.ID, -32602, "invalid rewind execute parameters")
		return
	}
	if req.Target == nil {
		req.Target = req.TargetSnake
	}
	if req.Target == nil {
		s.respondError(incoming.ID, -32602, "targetPromptIndex is required")
		return
	}
	mode := req.Mode
	if mode == "" {
		mode = "all"
	}
	if mode == "code_only" {
		mode = "files_only"
	}
	if mode != "all" && mode != "conversation_only" && mode != "files_only" {
		s.respondError(incoming.ID, -32602, "invalid rewind mode")
		return
	}
	current.mu.Lock()
	defer current.mu.Unlock()
	if current.running {
		s.respondError(incoming.ID, -32000, "cannot rewind while a prompt is running")
		return
	}
	preview, err := sessionlog.PreviewRewind(path, *req.Target)
	if err != nil {
		s.respond(incoming.ID, rewindResponse(*req.Target, mode, false, err.Error()))
		return
	}
	wantsFiles := mode == "all" || mode == "files_only"
	wantsConversation := mode == "all" || mode == "conversation_only"
	filePreview := workspace.FileRewindPreview{CleanFiles: []string{}, Conflicts: []workspace.RewindConflict{}}
	if wantsFiles {
		filePreview, err = current.rewind.Preview(*req.Target)
		if err != nil {
			s.respondError(incoming.ID, -32000, err.Error())
			return
		}
	}
	response := rewindResponse(*req.Target, mode, false, "")
	response["clean_files"] = filePreview.CleanFiles
	response["conflicts"] = filePreview.Conflicts
	if !req.Force {
		if len(filePreview.Conflicts) > 0 {
			response["error"] = "External modifications detected. Confirm to revert anyway."
		}
		s.respond(incoming.ID, response)
		return
	}
	if wantsFiles {
		reverted, latestPreview, restoreErr := current.rewind.Restore(*req.Target)
		if restoreErr != nil {
			s.respondError(incoming.ID, -32000, restoreErr.Error())
			return
		}
		response["reverted_files"] = reverted
		response["conflicts"] = latestPreview.Conflicts
	}
	if wantsConversation {
		result, rewindErr := sessionlog.Rewind(path, *req.Target)
		if rewindErr != nil {
			s.respondError(incoming.ID, -32000, rewindErr.Error())
			return
		}
		current.previous = result.PreviousResponseID
		current.runner.RewindHistory(result.Messages)
		current.promptIndex = *req.Target
		response["prompt_text"] = preview.PromptText
		s.notify(sessionID, map[string]any{"sessionUpdate": "rewind_marker", "target_prompt_index": *req.Target})
	}
	current.updated = time.Now().UTC()
	response["success"] = true
	response["clean_files"] = []string{}
	s.respond(incoming.ID, response)
}

func rewindResponse(target int, mode string, success bool, message string) map[string]any {
	var responseError any
	if message != "" {
		responseError = message
	}
	return map[string]any{
		"success": success, "target_prompt_index": target, "mode": mode,
		"reverted_files": []string{}, "clean_files": []string{}, "conflicts": []any{},
		"prompt_text": nil, "error": responseError,
	}
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
	ws, err := workspace.Open(sessionConfig.CWD)
	if err != nil {
		closeRuntime()
		return nil, err
	}
	sessionPath, pathErr := sessionlog.PathForID(s.SessionDir, id)
	if pathErr != nil {
		closeRuntime()
		return nil, pathErr
	}
	artifactDir, err := sessionlog.ArtifactDir(sessionPath)
	if err != nil {
		closeRuntime()
		return nil, err
	}
	if err := runner.Tools.ConfigureHunkState(artifactDir); err != nil {
		closeRuntime()
		return nil, err
	}
	checkpointPath := filepath.Join(filepath.Dir(sessionPath), "rewind", id+".jsonl")
	rewind, err := workspace.NewRewindStore(ws, checkpointPath)
	if err != nil {
		closeRuntime()
		return nil, err
	}
	promptIndex := 0
	if points, pointsErr := sessionlog.RewindPoints(sessionPath); pointsErr == nil {
		promptIndex = len(points)
	} else if !errors.Is(pointsErr, os.ErrNotExist) {
		closeRuntime()
		return nil, pointsErr
	}
	mode := "default"
	if storedMode, modeErr := sessionlog.CurrentMode(sessionPath); modeErr == nil {
		if validSessionMode(storedMode) {
			mode = storedMode
		}
	} else if !errors.Is(modeErr, os.ErrNotExist) {
		closeRuntime()
		return nil, modeErr
	}
	created := &session{
		id: id, cwd: sessionConfig.CWD, updated: time.Now().UTC(), previous: previous,
		runner: runner, close: closeRuntime, promptIndex: promptIndex, activePrompt: -1, rewind: rewind, logPath: sessionPath, mode: mode,
	}
	runner.SessionID = id
	runner.ToolObserver = &sessionToolObserver{server: s, sessionID: id}
	runner.Tools.SetRewindStore(rewind, func() int {
		created.mu.Lock()
		defer created.mu.Unlock()
		return created.activePrompt
	})
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
	prompt, content, err := renderPrompt(params.Prompt)
	if err != nil {
		s.respondError(incoming.ID, -32602, err.Error())
		return
	}
	if len(content) == 0 {
		s.respondError(incoming.ID, -32602, "prompt must contain text, resources, or images")
		return
	}
	if prompt == "" {
		prompt = "Image prompt"
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
	current.activePrompt = current.promptIndex
	current.promptIndex++
	if current.title == "" {
		current.title = promptTitle(prompt)
		s.notify(current.id, map[string]any{
			"sessionUpdate": "session_info_update", "title": current.title,
			"updatedAt": time.Now().UTC().Format(time.RFC3339),
		})
	}
	current.updated = time.Now().UTC()
	previous := current.previous
	mode := current.mode
	current.mu.Unlock()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		baseInstructions := current.runner.Instructions
		current.runner.Instructions = instructionsForMode(baseInstructions, mode)
		result, err := current.runner.RunTurnParts(runCtx, prompt, content, previous)
		current.runner.Instructions = baseInstructions
		points, pointsErr := sessionlog.RewindPoints(current.logPath)
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
		current.activePrompt = -1
		if pointsErr == nil {
			current.promptIndex = len(points)
		}
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
		SessionID  string           `json:"sessionId"`
		CWD        string           `json:"cwd"`
		MCPServers []mcpServerParam `json:"mcpServers"`
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
	model := ""
	for _, item := range items {
		if item.SessionID == params.SessionID {
			found = true
			model = item.ModelID
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
	servers, err := parseMCPServers(params.MCPServers)
	if err != nil {
		s.respondError(incoming.ID, -32602, err.Error())
		return
	}
	config := SessionConfig{CWD: params.CWD, Model: model, SessionID: params.SessionID, ResumePath: path, MCPServers: servers}
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
			if historical.Role != "user" || len(historical.Content) == 0 {
				s.notify(params.SessionID, map[string]any{
					"sessionUpdate": updateType,
					"content":       map[string]any{"type": "text", "text": historical.Text},
				})
				continue
			}
			for _, part := range historical.Content {
				content := map[string]any{"type": part.Type}
				if part.Type == "text" {
					content["text"] = part.Text
				} else if part.Data != "" {
					content["data"] = part.Data
					content["mimeType"] = part.MimeType
				} else {
					content["uri"] = part.URI
				}
				s.notify(params.SessionID, map[string]any{
					"sessionUpdate": updateType,
					"content":       content,
				})
			}
		}
	}
	current := s.lookupSession(params.SessionID)
	mode := "default"
	if current != nil {
		current.mu.Lock()
		mode = current.mode
		current.mu.Unlock()
	}
	s.respond(incoming.ID, map[string]any{"sessionId": params.SessionID, "modes": sessionModes(mode)})
}

func (s *Server) handleSetMode(incoming message) {
	var params struct {
		SessionID string `json:"sessionId"`
		ModeID    string `json:"modeId"`
	}
	if json.Unmarshal(incoming.Params, &params) != nil || params.SessionID == "" || !validSessionMode(params.ModeID) {
		s.respondError(incoming.ID, -32602, "sessionId and a valid modeId are required")
		return
	}
	current := s.lookupSession(params.SessionID)
	if current == nil {
		s.respondError(incoming.ID, -32602, "unknown session")
		return
	}
	current.mu.Lock()
	if current.mode == params.ModeID {
		current.mu.Unlock()
		s.respond(incoming.ID, map[string]any{})
		return
	}
	if current.runner.Logger != nil {
		if err := current.runner.Logger.Append("session_mode", map[string]any{"mode_id": params.ModeID}); err != nil {
			current.mu.Unlock()
			s.respondError(incoming.ID, -32000, err.Error())
			return
		}
	}
	current.mode = params.ModeID
	current.mu.Unlock()
	s.notify(params.SessionID, map[string]any{"sessionUpdate": "current_mode_update", "currentModeId": params.ModeID})
	s.respond(incoming.ID, map[string]any{})
}

func validSessionMode(mode string) bool {
	return mode == "default" || mode == "ask" || mode == "plan"
}

func sessionModes(current string) map[string]any {
	return map[string]any{
		"currentModeId": current,
		"availableModes": []any{
			map[string]any{"id": "default", "name": "Agent", "description": "Use tools to complete the task."},
			map[string]any{"id": "ask", "name": "Ask", "description": "Answer without changing the workspace."},
			map[string]any{"id": "plan", "name": "Plan", "description": "Investigate and produce an implementation plan without changing the workspace."},
		},
	}
}

func instructionsForMode(base, mode string) string {
	var instruction string
	switch mode {
	case "ask":
		instruction = "Session mode: ask. Answer the user's question without editing files or running commands that change the workspace."
	case "plan":
		instruction = "Session mode: plan. Investigate as needed and return a concrete implementation plan. Do not edit files or run commands that change the workspace."
	default:
		return base
	}
	if strings.TrimSpace(base) == "" {
		return instruction
	}
	return base + "\n\n" + instruction
}

func parseMCPServers(params []mcpServerParam) ([]MCPServer, error) {
	servers := make([]MCPServer, 0, len(params))
	for _, param := range params {
		if param.Name == "" {
			return nil, errors.New("MCP servers require a name")
		}
		switch param.Type {
		case "":
			if param.Command == "" {
				return nil, fmt.Errorf("stdio MCP server %q requires a command", param.Name)
			}
			env := make(map[string]string, len(param.Env))
			for _, entry := range param.Env {
				if entry.Name == "" {
					return nil, fmt.Errorf("stdio MCP server %q has an empty environment name", param.Name)
				}
				env[entry.Name] = entry.Value
			}
			servers = append(servers, MCPServer{Name: param.Name, Command: param.Command, Args: param.Args, Env: env})
		case "http", "sse":
			endpoint, err := url.Parse(param.URL)
			if err != nil || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Host == "" {
				return nil, fmt.Errorf("%s MCP server %q requires a valid HTTP(S) URL", param.Type, param.Name)
			}
			headers := make(map[string]string, len(param.Headers))
			for _, header := range param.Headers {
				if !validHTTPHeaderName(header.Name) || strings.ContainsAny(header.Value, "\r\n") {
					return nil, fmt.Errorf("%s MCP server %q has an invalid header", param.Type, param.Name)
				}
				headers[http.CanonicalHeaderKey(header.Name)] = header.Value
			}
			servers = append(servers, MCPServer{Type: param.Type, Name: param.Name, URL: endpoint.String(), Headers: headers})
		default:
			return nil, fmt.Errorf("MCP server %q has unsupported type %q", param.Name, param.Type)
		}
	}
	return servers, nil
}

func validHTTPHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for _, char := range name {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", char) {
			continue
		}
		return false
	}
	return true
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

func renderPrompt(blocks []promptBlock) (string, []api.ContentPart, error) {
	var textParts []string
	content := make([]api.ContentPart, 0, len(blocks))
	addText := func(text string) {
		textParts = append(textParts, text)
		content = append(content, api.ContentPart{Type: "input_text", Text: text})
	}
	for _, block := range blocks {
		switch block.Type {
		case "text":
			if block.Text != "" {
				addText(block.Text)
			}
		case "resource_link":
			if block.Name == "" || block.URI == "" {
				return "", nil, errors.New("resource links require name and uri")
			}
			addText(fmt.Sprintf("Referenced resource %s: %s", block.Name, block.URI))
		case "resource":
			if block.Resource.URI == "" {
				return "", nil, errors.New("embedded resources require a uri")
			}
			if block.Resource.Text == "" {
				if block.Resource.Blob != "" {
					return "", nil, errors.New("binary embedded resources are not supported")
				}
				return "", nil, errors.New("embedded text resources require text")
			}
			header := "Embedded resource " + block.Resource.URI
			if block.Resource.MimeType != "" {
				header += " (" + block.Resource.MimeType + ")"
			}
			addText(header + ":\n" + block.Resource.Text)
		case "image":
			imageURL, err := promptImageURL(block)
			if err != nil {
				return "", nil, err
			}
			content = append(content, api.ContentPart{Type: "input_image", ImageURL: imageURL})
		case "audio":
			return "", nil, errors.New("audio prompt content is not supported")
		default:
			return "", nil, fmt.Errorf("unsupported prompt content type %q", block.Type)
		}
	}
	return strings.TrimSpace(strings.Join(textParts, "\n\n")), content, nil
}

func promptImageURL(block promptBlock) (string, error) {
	if block.Data == "" {
		if strings.HasPrefix(block.URI, "https://") || strings.HasPrefix(block.URI, "http://") {
			return block.URI, nil
		}
		return "", errors.New("images require base64 data or an HTTP(S) uri")
	}
	if block.MimeType != "image/png" && block.MimeType != "image/jpeg" && block.MimeType != "image/gif" && block.MimeType != "image/webp" {
		return "", fmt.Errorf("unsupported image mime type %q", block.MimeType)
	}
	if base64.StdEncoding.DecodedLen(len(block.Data)) > 20<<20 {
		return "", errors.New("image prompt exceeds 20 MB")
	}
	if _, err := base64.StdEncoding.DecodeString(block.Data); err != nil {
		return "", errors.New("image prompt data is not valid base64")
	}
	return "data:" + block.MimeType + ";base64," + block.Data, nil
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

func (s *Server) handleTerminal(incoming message) {
	if s.terminals == nil {
		s.respondError(incoming.ID, -32000, "terminal manager is unavailable")
		return
	}
	switch incoming.Method {
	case "x.ai/terminal/pty/create":
		var req struct {
			Shell     string `json:"shell"`
			CWD       string `json:"cwd"`
			SessionID string `json:"sessionId"`
			Env       []struct {
				Name  string `json:"name"`
				Value string `json:"value"`
			} `json:"env"`
			Rows *uint16 `json:"rows"`
			Cols *uint16 `json:"cols"`
			Name string  `json:"name"`
		}
		if json.Unmarshal(incoming.Params, &req) != nil {
			s.respondError(incoming.ID, -32602, "invalid PTY create parameters")
			return
		}
		if req.CWD == "" && req.SessionID != "" {
			if current := s.lookupSession(req.SessionID); current != nil {
				req.CWD = current.cwd
			}
		}
		env := make(map[string]string, len(req.Env))
		for _, item := range req.Env {
			env[item.Name] = item.Value
		}
		id, err := s.terminals.create(req.Shell, req.CWD, env, defaultTerminalDimension(req.Rows, 24), defaultTerminalDimension(req.Cols, 80), req.Name)
		if err != nil {
			s.respondError(incoming.ID, -32000, err.Error())
			return
		}
		s.respond(incoming.ID, map[string]any{"terminalId": id})
	case "x.ai/terminal/pty/load":
		var req struct {
			TerminalID string `json:"terminalId"`
		}
		if json.Unmarshal(incoming.Params, &req) != nil || req.TerminalID == "" {
			s.respondError(incoming.ID, -32602, "terminalId is required")
			return
		}
		result, err := s.terminals.load(req.TerminalID)
		if err != nil {
			s.respondError(incoming.ID, -32000, err.Error())
			return
		}
		s.respond(incoming.ID, result)
	case "x.ai/terminal/pty/resize":
		var req struct {
			TerminalID string `json:"terminalId"`
			Rows       uint16 `json:"rows"`
			Cols       uint16 `json:"cols"`
		}
		if json.Unmarshal(incoming.Params, &req) != nil || req.TerminalID == "" {
			s.respondError(incoming.ID, -32602, "terminalId, rows and cols are required")
			return
		}
		if err := s.terminals.resize(req.TerminalID, req.Rows, req.Cols); err != nil {
			s.respondError(incoming.ID, -32000, err.Error())
			return
		}
		s.respond(incoming.ID, map[string]any{})
	case "x.ai/terminal/list":
		s.respond(incoming.ID, map[string]any{"terminals": s.terminals.list()})
	case "x.ai/terminal/kill":
		var req struct {
			TerminalID string `json:"terminalId"`
		}
		if json.Unmarshal(incoming.Params, &req) != nil || req.TerminalID == "" {
			s.respondError(incoming.ID, -32602, "terminalId is required")
			return
		}
		outcome, err := s.terminals.kill(req.TerminalID)
		if err != nil {
			s.respondError(incoming.ID, -32000, err.Error())
			return
		}
		s.respond(incoming.ID, map[string]any{"outcome": outcome})
	}
}

func defaultTerminalDimension(value *uint16, fallback uint16) uint16 {
	if value == nil {
		return fallback
	}
	return *value
}

func (s *Server) handleTerminalInput(raw json.RawMessage) {
	if s.terminals == nil {
		return
	}
	var req struct {
		TerminalID string `json:"terminalId"`
		Data       string `json:"data"`
	}
	if json.Unmarshal(raw, &req) != nil || req.TerminalID == "" {
		return
	}
	data, err := base64.StdEncoding.DecodeString(req.Data)
	if err != nil {
		return
	}
	_ = s.terminals.writeInput(req.TerminalID, data)
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
	if s.terminals != nil {
		s.terminals.closeAll()
	}
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

func (o *sessionToolObserver) ToolFinished(call api.ToolCall, result tools.ExecutionResult, toolErr error) {
	status := "completed"
	if toolErr != nil {
		status = "failed"
		if result.Output == "" {
			result.Output = toolErr.Error()
		}
	}
	update := map[string]any{
		"sessionUpdate": "tool_call_update", "toolCallId": call.CallID,
		"status": status, "rawOutput": result.Output,
	}
	if len(result.Images) > 0 {
		content := make([]any, 0, len(result.Images))
		for _, image := range result.Images {
			content = append(content, map[string]any{
				"type": "content",
				"content": map[string]any{
					"type": "image", "data": base64.StdEncoding.EncodeToString(image.Data), "mimeType": image.MediaType,
				},
			})
		}
		update["content"] = content
	}
	o.server.notify(o.sessionID, update)
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
