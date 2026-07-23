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
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/api"
	"github.com/lookcorner/go-cli/internal/auth"
	"github.com/lookcorner/go-cli/internal/hooks"
	mcppkg "github.com/lookcorner/go-cli/internal/mcp"
	sessionlog "github.com/lookcorner/go-cli/internal/session"
	"github.com/lookcorner/go-cli/internal/terminaldiag"
	"github.com/lookcorner/go-cli/internal/tools"
	"github.com/lookcorner/go-cli/internal/workspace"
	worktrees "github.com/lookcorner/go-cli/internal/worktree"
)

const ProtocolVersion = 1

var clientSessionIDPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

type SessionConfig struct {
	CWD             string
	DisplayCWD      string
	Title           string
	Model           string
	ReasoningEffort string
	MCPServers      []MCPServer
	MCPSDKServers   []MCPServer
	SessionID       string
	ResumePath      string
	YoloMode        *bool
	AutoMode        *bool
	ClientHooks     []hooks.ClientHookGroup
	MCPInitProgress func(total, connected int)
	MCPReverseCall  MCPReverseCall
}

func sessionPermissionModeOverrides(meta map[string]any) (yoloMode, autoMode *bool) {
	if value, ok := meta["yoloMode"].(bool); ok {
		yoloMode = &value
	}
	value, exists := meta["autoMode"]
	if !exists {
		value = meta["auto_mode"]
	}
	if value, ok := value.(bool); ok {
		autoMode = &value
	}
	return yoloMode, autoMode
}

func stringMeta(meta map[string]any, key string) string {
	value, _ := meta[key].(string)
	return strings.TrimSpace(value)
}

func sessionStartupOverrides(meta map[string]any) (sessionID, model string, err error) {
	if value, ok := meta["sessionId"].(string); ok {
		if !clientSessionIDPattern.MatchString(value) {
			return "", "", fmt.Errorf("invalid UUID format for _meta.sessionId %q", value)
		}
		sessionID = value
	}
	if value, ok := meta["modelId"].(string); ok && value != "" {
		model = value
	}
	return sessionID, model, nil
}

type MCPServer = mcppkg.ServerConfig
type MCPReverseCall func(context.Context, string, json.RawMessage) (json.RawMessage, error)

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
	Factory            Factory
	Auth               AuthConfig
	Bundle             BundleConfig
	AuthChanged        func(context.Context, auth.LogoutResult) error
	Initialized        func()
	BillingMeta        func() (*bool, *string)
	SharingEnabled     func() bool
	SessionDir         string
	FolderTrustEnabled bool
	input              io.Reader
	output             io.Writer
	writeMu            sync.Mutex
	pathRewriters      sync.Map
	initializedOnce    sync.Once
	initialized        atomic.Bool
	announcements      announcementState
	authMu             sync.RWMutex
	mu                 sync.Mutex
	sessions           map[string]*session
	pending            map[string]chan permissionResult
	pendingPlan        map[string]chan planApprovalResult
	pendingQuestion    map[string]chan userQuestionResult
	pendingHook        map[string]chan clientHookResult
	pendingTrust       map[string]chan folderTrustResult
	pendingMCP         map[string]chan mcpReverseResult
	clientFS           *clientFSConfig
	clientGitHead      bool
	clientFolderTrust  bool
	trustMu            sync.Mutex
	trustPrompted      map[string]bool
	trustPromptTimeout time.Duration
	trustContext       context.Context
	trustCancel        context.CancelFunc
	nextSession        atomic.Uint64
	nextRequest        atomic.Uint64
	closing            atomic.Bool
	wg                 sync.WaitGroup
	worktrees          *worktrees.Manager
	terminals          *terminalManager
	fuzzySearch        *workspace.FuzzySearchManager
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
	id                  string
	ctx                 context.Context
	cwd                 string
	displayCWD          string
	title               string
	updated             time.Time
	runner              *agent.Runner
	close               func()
	mu                  sync.Mutex
	previous            string
	cancel              context.CancelFunc
	running             bool
	pendingInteractions int
	runDone             chan struct{}
	btwCancel           context.CancelFunc
	btwDone             chan struct{}
	recapCancel         context.CancelFunc
	recapDone           chan struct{}
	suggestCancel       context.CancelFunc
	suggestDone         chan struct{}
	fileWatchCancel     context.CancelFunc
	fileWatchDone       chan struct{}
	gitWatchCancel      context.CancelFunc
	gitWatchDone        chan struct{}
	gitHeadEnabled      bool
	lastGitHead         string
	lastRecapPrompt     int
	promptIndex         int
	activePrompt        int
	inputTokens         int
	rewind              *workspace.RewindStore
	logPath             string
	mode                string
	modeBeforeYolo      tools.PermissionMode
	mcpServers          []MCPServer
	wakeQueue           []syntheticWake
	interjectionQueue   []agent.Interjection
	promptQueue         []queuedPrompt
	runningPromptID     string
	startingPromptID    string
	cancelTrigger       string
	activeWakeID        string
	closed              bool
	unavailableModel    string
	pendingModelID      string
	permissions         *serverApprover
}

type syntheticWake struct {
	id            string
	prompt        string
	monitorEvents []tools.MonitorEvent
}

type permissionResult struct {
	outcome  string
	optionID string
	err      error
}

type planApprovalResult struct {
	decision tools.PlanModeDecision
	err      error
}

type userQuestionResult struct {
	response tools.UserQuestionResponse
	err      error
}

type mcpReverseResult struct {
	result json.RawMessage
	err    error
}

func (s *Server) WorktreeManager() *worktrees.Manager { return s.worktrees }

type promptBlock struct {
	Type     string         `json:"type"`
	Text     string         `json:"text"`
	Data     string         `json:"data"`
	MimeType string         `json:"mimeType"`
	URI      string         `json:"uri"`
	Name     string         `json:"name"`
	Meta     map[string]any `json:"_meta,omitempty"`
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
	s.closing.Store(false)
	s.sessions = make(map[string]*session)
	s.pending = make(map[string]chan permissionResult)
	s.pendingPlan = make(map[string]chan planApprovalResult)
	s.pendingQuestion = make(map[string]chan userQuestionResult)
	s.pendingHook = make(map[string]chan clientHookResult)
	s.pendingTrust = make(map[string]chan folderTrustResult)
	s.pendingMCP = make(map[string]chan mcpReverseResult)
	s.clientFS = nil
	s.clientGitHead = false
	s.clientFolderTrust = false
	s.trustPrompted = make(map[string]bool)
	s.trustContext, s.trustCancel = context.WithCancel(ctx)
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
			s.clientFS = parseClientFS(incoming.Params)
			s.clientGitHead = parseClientGitHead(incoming.Params)
			s.clientFolderTrust = parseClientFolderTrust(incoming.Params)
			authConfig := s.authSnapshot()
			meta := map[string]any{"availableCommands": availableCommands(nil, false)}
			if authConfig.DefaultMethodID != "" {
				meta["defaultAuthMethodId"] = authConfig.DefaultMethodID
			}
			meta["x.ai/mcp/sdk"] = true
			s.respond(incoming.ID, map[string]any{
				"protocolVersion": ProtocolVersion,
				"agentCapabilities": map[string]any{
					"loadSession":         true,
					"promptCapabilities":  map[string]any{"image": true, "audio": false, "embeddedContext": true},
					"mcpCapabilities":     map[string]any{"http": true, "sse": true},
					"sessionCapabilities": map[string]any{"close": map[string]any{}, "list": map[string]any{}, "resume": map[string]any{}},
					"auth":                map[string]any{},
					"_meta": map[string]any{
						"x.ai/fs_notify": true,
						"x.ai/hooks":     map[string]any{"blockingEvents": []any{"pre_tool_use"}, "decisions": []any{"deny"}},
					},
				},
				"authMethods": authConfig.Methods,
				"agentInfo":   map[string]any{"name": "gork-go", "version": "0.1.0-dev"},
				"_meta":       meta,
			})
			s.initializedOnce.Do(func() {
				s.initialized.Store(true)
				s.SeedAnnouncements()
				if s.Initialized != nil {
					s.Initialized()
				}
			})
		case "authenticate":
			s.handleAuthenticate(ctx, incoming)
		case "x.ai/bundle/sync", "x.ai/bundle/status", "x.ai/bundle/entry/get":
			s.handleBundle(ctx, incoming)
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
		case "session/set_model":
			s.handleSetSessionModel(incoming)
		case "x.ai/toggle_plan_mode":
			s.handleTogglePlanMode(incoming.Params)
		case "x.ai/yolo_mode_changed":
			s.handleYoloModeChanged(incoming.Params)
		case "x.ai/permissions/reset":
			s.handlePermissionReset()
		case "x.ai/debug/arm_auto_compact", "x.ai/debug/trigger_feedback":
			s.handleDebug(incoming)
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
		case "x.ai/git/git_repo_root", "x.ai/git/status", "x.ai/git/stage", "x.ai/git/stage/content", "x.ai/git/unstage", "x.ai/git/discard", "x.ai/git/current_commit", "x.ai/git/info", "x.ai/git/branches", "x.ai/git/stash", "x.ai/git/checkout", "x.ai/git/checkout_session_head", "x.ai/git/checkout_commit", "x.ai/git/commit", "x.ai/git/files", "x.ai/git/diffs", "x.ai/git/serialize_changes":
			s.handleGit(ctx, incoming)
		case "x.ai/pr/status":
			s.handlePRStatus(ctx, incoming)
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
		case "x.ai/session/repair":
			s.handleSessionRepair(incoming)
		case "x.ai/session/updates":
			s.handleSessionUpdates(incoming)
		case "x.ai/session/info", "x.ai/session/rename", "x.ai/session/delete", "x.ai/session/search", "x.ai/prompt_history":
			s.handleSessionAdmin(incoming)
		case "x.ai/session/update_mcp_servers":
			s.handleUpdateMCPServers(ctx, incoming)
		case "x.ai/internal/reload_all_mcp_servers", "x.ai/internal/reload_project_mcp_servers":
			s.handleMCPReload(ctx, incoming)
		case "x.ai/internal/reload_models", "x.ai/internal/reload_models_cache":
			s.handleModelReload(incoming)
		case "x.ai/internal/evict_sessions":
			s.handleEvictSessions(incoming.Params)
		case "x.ai/auth/info", "x.ai/auth/getBearerToken", "x.ai/auth/get_url", "x.ai/auth/submit_code", "x.ai/auth/logout", "x.ai/auth/check_subscription", "x.ai/internal/auth_cleared", "x.ai/getApiKey", "x.ai/setApiKey":
			s.handleAuth(ctx, incoming)
		case "x.ai/privacy/setCodingDataRetention":
			s.handlePrivacy(ctx, incoming)
		case "x.ai/billing", "x.ai/auto-topup-rule":
			s.handleBilling(ctx, incoming)
		case "x.ai/cloud/env/list", "x.ai/cloud/env/create", "x.ai/cloud/env/update", "x.ai/cloud/env/delete", "x.ai/cloud/terminate":
			s.handleCloud(ctx, incoming)
		case "x.ai/share_session":
			s.handleShareSession(ctx, incoming)
		case "x.ai/session_summaries/session_list", "x.ai/session_summaries/workspace_list", "x.ai/session_summaries/workspace_list_recent":
			s.handleSessionSummaries(incoming)
		case "x.ai/sessions/list":
			s.handleSessionRoster(ctx, incoming)
		case "x.ai/session/list":
			s.handleUnifiedSessionList(incoming)
		case "x.ai/session/close":
			s.handleExtensionSessionClose(incoming)
		case "x.ai/search/content":
			s.handleContentSearch(ctx, incoming)
		case "x.ai/search/fuzzy/open", "x.ai/search/fuzzy/change", "x.ai/search/fuzzy/close":
			s.handleFuzzySearch(ctx, incoming)
		case "x.ai/code/goto-definition", "x.ai/code/goto-references", "x.ai/code/find-definitions", "x.ai/code/find-references", "x.ai/code/status":
			s.handleCodeNavigation(ctx, incoming)
		case "x.ai/mcp/list", "x.ai/mcp/call", "x.ai/mcp/read_resource", "x.ai/mcp/auth_status", "x.ai/mcp/auth_trigger", "x.ai/mcp/toggle", "x.ai/mcp/toggle_tool", "x.ai/mcp/upsert", "x.ai/mcp/delete":
			s.handleMCP(ctx, incoming)
		case "x.ai/commands/list":
			s.handleCommands(incoming)
		case "x.ai/workspaces/list":
			s.handleStaticExtension(incoming)
		case "x.ai/compact_conversation", "x.ai/memory/flush", "x.ai/memory/rewrite":
			s.handleMemoryExtension(ctx, incoming)
		case "x.ai/btw":
			s.handleBtw(ctx, incoming)
		case "x.ai/feedback", "x.ai/feedback/dismiss":
			s.handleFeedback(incoming)
		case "x.ai/rollout/survey":
			s.handleRolloutSurvey(incoming)
		case "x.ai/review/comment", "x.ai/review/comment/delete":
			s.handleReview(incoming)
		case "x.ai/interject":
			s.handleInterject(incoming)
		case "x.ai/recap":
			s.handleRecap(ctx, incoming)
		case "x.ai/suggestPrompt":
			s.handleSuggestPrompt(ctx, incoming)
		case "x.ai/suggest":
			s.handleSuggest(ctx, incoming)
		case "x.ai/queue/remove", "x.ai/queue/reorder", "x.ai/queue/clear", "x.ai/queue/edit", "x.ai/queue/interject":
			s.handleQueueUpdate(incoming)
		case "x.ai/skills/list", "x.ai/skills/config", "x.ai/skills/add", "x.ai/skills/remove", "x.ai/skills/reset", "x.ai/skills/toggle", "x.ai/skills/refresh-baseline", "x.ai/internal/reload_skills":
			s.handleSkills(ctx, incoming)
		case "x.ai/plugins/list", "x.ai/plugins/action", "x.ai/plugins/notify-updates", "x.ai/plugins/reload":
			s.handlePlugins(ctx, incoming)
		case "x.ai/hooks/list", "x.ai/hooks/action":
			s.handleHooks(ctx, incoming)
		case "x.ai/task/list", "x.ai/task/kill":
			s.handleTasks(ctx, incoming)
		case "x.ai/scheduler/delete":
			s.handleScheduler(incoming)
		case "x.ai/subagent/get", "x.ai/subagent/list_running", "x.ai/subagent/cancel":
			s.handleSubagents(ctx, incoming)
		case "x.ai/marketplace/list", "x.ai/marketplace/action":
			s.handleMarketplace(ctx, incoming)
		case "x.ai/fs/list", "x.ai/fs/exists", "x.ai/fs/read_file", "x.ai/fs/write_file", "x.ai/fs/delete_file":
			s.handleFS(incoming)
		case "x.ai/rewind/points", "x.ai/rewind/execute":
			s.handleRewind(incoming)
		case "x.ai/terminal/create", "x.ai/terminal/output", "x.ai/terminal/wait_for_exit", "x.ai/terminal/release", "x.ai/terminal/background", "x.ai/terminal/pty/create", "x.ai/terminal/pty/load", "x.ai/terminal/pty/resize", "x.ai/terminal/list", "x.ai/terminal/kill":
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

func (s *Server) handleSessionAdmin(incoming message) {
	var req struct {
		SessionID            string `json:"sessionId"`
		Title                string `json:"title"`
		CWD                  string `json:"cwd"`
		Query                string `json:"query"`
		Limit                int    `json:"limit"`
		Offset               int    `json:"offset"`
		IncludeContent       bool   `json:"includeContent"`
		PromptSessionID      string `json:"session_id"`
		FilterSessionID      string `json:"filter_session_id"`
		FilterSessionIDCamel string `json:"filterSessionId"`
	}
	if json.Unmarshal(incoming.Params, &req) != nil {
		s.respondError(incoming.ID, -32602, "invalid session parameters")
		return
	}
	extResult := func(value any, err error) {
		if err != nil {
			s.respond(incoming.ID, map[string]any{"result": nil, "error": err.Error()})
		} else {
			s.respond(incoming.ID, map[string]any{"result": value})
		}
	}
	switch incoming.Method {
	case "x.ai/session/info":
		id := req.SessionID
		if id == "" {
			s.mu.Lock()
			for candidate := range s.sessions {
				id = candidate
				break
			}
			s.mu.Unlock()
		}
		if id == "" {
			extResult(map[string]any{}, nil)
			return
		}
		if current := s.lookupSession(id); current != nil {
			current.mu.Lock()
			used, total := current.inputTokens, current.runner.ContextWindow
			turns, turnIndex := current.promptIndex, current.activePrompt
			if turnIndex < 0 {
				turnIndex = turns
			}
			result := map[string]any{
				"sessionId": id, "cwd": current.cwd, "model": current.runner.Model,
				"turns": turns, "turnIndex": turnIndex,
				"context": sessionContextWire(used, total, turns),
			}
			current.mu.Unlock()
			extResult(result, nil)
			return
		}
		info, err := sessionlog.InfoByID(s.SessionDir, id)
		if err != nil {
			extResult(map[string]any{}, nil)
			return
		}
		path, _ := sessionlog.PathForID(s.SessionDir, id)
		points, _ := sessionlog.RewindPoints(path)
		extResult(map[string]any{
			"sessionId": id, "cwd": info.CWD, "model": info.ModelID,
			"turns": len(points), "turnIndex": len(points), "context": sessionContextWire(0, 0, len(points)),
		}, nil)
	case "x.ai/session/rename":
		if req.SessionID == "" {
			s.respondError(incoming.ID, -32602, "sessionId is required")
			return
		}
		if err := sessionlog.Rename(s.SessionDir, req.SessionID, req.Title); err != nil {
			s.respondError(incoming.ID, -32000, err.Error())
			return
		}
		if current := s.lookupSession(req.SessionID); current != nil {
			current.mu.Lock()
			current.title = strings.TrimSpace(req.Title)
			current.updated = time.Now().UTC()
			current.mu.Unlock()
			s.notify(req.SessionID, map[string]any{
				"sessionUpdate": "session_info_update", "title": strings.TrimSpace(req.Title),
				"updatedAt": time.Now().UTC().Format(time.RFC3339),
			})
		}
		s.respond(incoming.ID, map[string]any{"success": true})
	case "x.ai/session/delete":
		if req.SessionID == "" {
			s.respondError(incoming.ID, -32602, "sessionId is required")
			return
		}
		s.mu.Lock()
		current := s.sessions[req.SessionID]
		delete(s.sessions, req.SessionID)
		s.mu.Unlock()
		if current != nil {
			current.mu.Lock()
			if current.cancel != nil {
				current.cancel()
			}
			current.mu.Unlock()
			current.close()
		}
		if err := sessionlog.Delete(s.SessionDir, req.SessionID); err != nil {
			s.respondError(incoming.ID, -32000, err.Error())
			return
		}
		s.respond(incoming.ID, map[string]any{"success": true})
	case "x.ai/session/search":
		result, err := sessionlog.Search(s.SessionDir, sessionlog.SearchRequest{
			Query: req.Query, CWD: req.CWD, Limit: req.Limit, Offset: req.Offset, IncludeContent: req.IncludeContent,
		})
		extResult(result, err)
	case "x.ai/prompt_history":
		sessionID, newestFirst := req.PromptSessionID, false
		if sessionID == "" {
			sessionID = req.SessionID
		}
		filterSessionID := req.FilterSessionID
		if filterSessionID == "" {
			filterSessionID = req.FilterSessionIDCamel
		}
		if filterSessionID != "" {
			sessionID, newestFirst = filterSessionID, true
		} else if sessionID == "" {
			newestFirst = true
		}
		prompts, err := sessionlog.PromptHistory(s.SessionDir, req.CWD, sessionID, newestFirst)
		if err != nil {
			s.respondError(incoming.ID, -32602, err.Error())
		} else {
			s.respond(incoming.ID, map[string]any{"prompts": prompts})
		}
	}
}

func (s *Server) handleUpdateMCPServers(ctx context.Context, incoming message) {
	var req struct {
		SessionID  string           `json:"sessionId"`
		MCPServers []mcpServerParam `json:"mcpServers"`
	}
	if json.Unmarshal(incoming.Params, &req) != nil || req.SessionID == "" {
		s.respondError(incoming.ID, -32602, "sessionId is required")
		return
	}
	current := s.lookupSession(req.SessionID)
	if current == nil || current.runner == nil || current.runner.UpdateMCPServers == nil {
		s.respondError(incoming.ID, -32602, "session does not support MCP updates")
		return
	}
	current.mu.Lock()
	running := current.running
	updater := current.runner.UpdateMCPServers
	current.mu.Unlock()
	if running {
		s.respondError(incoming.ID, -32000, "cannot update MCP servers while a prompt is running")
		return
	}
	servers, err := parseMCPServers(req.MCPServers)
	if err != nil {
		s.respondError(incoming.ID, -32602, err.Error())
		return
	}
	if err := updater(ctx, servers); err != nil {
		s.respondError(incoming.ID, -32000, err.Error())
		return
	}
	current.mu.Lock()
	current.mcpServers = append([]MCPServer(nil), servers...)
	current.mu.Unlock()
	s.respond(incoming.ID, map[string]any{"result": map[string]any{"ok": true}, "error": nil})
}

func (s *Server) handleSessionSummaries(incoming message) {
	var req struct {
		WorkspaceDirectory string `json:"workspace_directory"`
		Limit              *int   `json:"limit"`
	}
	if json.Unmarshal(incoming.Params, &req) != nil {
		s.respondError(incoming.ID, -32602, "invalid session summaries parameters")
		return
	}
	switch incoming.Method {
	case "x.ai/session_summaries/session_list":
		if req.WorkspaceDirectory == "" {
			s.respondError(incoming.ID, -32602, "workspace_directory is required")
			return
		}
		summaries, err := sessionlog.Summaries(s.SessionDir, req.WorkspaceDirectory, 0)
		if err != nil {
			s.respondError(incoming.ID, -32000, err.Error())
		} else {
			s.respond(incoming.ID, map[string]any{"session_summaries": summaries})
		}
	case "x.ai/session_summaries/workspace_list":
		summaries, err := sessionlog.Summaries(s.SessionDir, "", 0)
		if err != nil {
			s.respondError(incoming.ID, -32000, err.Error())
			return
		}
		grouped := make(map[string][]sessionlog.Summary)
		for _, summary := range summaries {
			grouped[summary.Info.CWD] = append(grouped[summary.Info.CWD], summary)
		}
		s.respond(incoming.ID, map[string]any{"all_sessions": grouped})
	case "x.ai/session_summaries/workspace_list_recent":
		if req.Limit == nil || *req.Limit < 0 {
			s.respondError(incoming.ID, -32602, "limit must not be negative")
			return
		}
		if *req.Limit == 0 {
			s.respond(incoming.ID, []sessionlog.Summary{})
			return
		}
		limit := *req.Limit
		if limit > 10_000 {
			limit = 10_000
		}
		summaries, err := sessionlog.Summaries(s.SessionDir, "", limit)
		if err != nil {
			s.respondError(incoming.ID, -32000, err.Error())
		} else {
			s.respond(incoming.ID, summaries)
		}
	}
}

func (s *Server) handleStaticExtension(incoming message) {
	switch incoming.Method {
	case "x.ai/workspaces/list":
		var req struct {
			PageSize  *int   `json:"pageSize"`
			PageToken string `json:"pageToken"`
			Query     string `json:"query"`
			Kind      string `json:"kind"`
		}
		if json.Unmarshal(incoming.Params, &req) != nil {
			s.respondError(incoming.ID, -32602, "invalid workspace list parameters")
			return
		}
		s.respond(incoming.ID, map[string]any{"result": map[string]any{
			"workspaces": []any{},
			"_meta":      map[string]any{"x.ai/partial": map[string]any{"workspaces": true, "reason": "no_oauth"}},
		}})
	}
}

func (s *Server) handleFS(incoming message) {
	var req struct {
		SessionID        string   `json:"sessionId"`
		Path             string   `json:"path"`
		Depth            int      `json:"depth"`
		IncludeHidden    *bool    `json:"includeHidden"`
		Limit            int      `json:"limit"`
		Offset           *int64   `json:"offset"`
		FollowSymlinks   *bool    `json:"followSymlinks"`
		RespectGitIgnore *bool    `json:"respectGitIgnore"`
		IncludeGlobs     []string `json:"includeGlobs"`
		ExcludeGlobs     []string `json:"excludeGlobs"`
		MaxBytes         int      `json:"maxBytes"`
		MaxLines         *uint64  `json:"maxLines"`
		Length           *uint64  `json:"length"`
		Encoding         string   `json:"encoding"`
		Content          string   `json:"content"`
		CreateDirs       *bool    `json:"createDirs"`
	}
	if json.Unmarshal(incoming.Params, &req) != nil || req.Path == "" {
		s.respondError(incoming.ID, -32602, "path is required")
		return
	}
	current := s.lookupSession(req.SessionID)
	if current == nil && filepath.IsAbs(req.Path) {
		s.mu.Lock()
		for _, candidate := range s.sessions {
			rel, err := filepath.Rel(candidate.cwd, req.Path)
			if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				current = candidate
				break
			}
		}
		s.mu.Unlock()
	}
	if current == nil {
		s.respondError(incoming.ID, -32602, "sessionId is required for relative paths")
		return
	}
	ws, err := workspace.Open(current.cwd)
	if err != nil {
		s.respondError(incoming.ID, -32000, err.Error())
		return
	}
	extResult := func(value any, err error) {
		if err != nil {
			s.respond(incoming.ID, map[string]any{"result": nil, "error": err.Error()})
		} else {
			s.respond(incoming.ID, map[string]any{"result": value})
		}
	}
	switch incoming.Method {
	case "x.ai/fs/list":
		includeHidden, followSymlinks, respectGitIgnore := true, true, true
		if req.IncludeHidden != nil {
			includeHidden = *req.IncludeHidden
		}
		if req.FollowSymlinks != nil {
			followSymlinks = *req.FollowSymlinks
		}
		if req.RespectGitIgnore != nil {
			respectGitIgnore = *req.RespectGitIgnore
		}
		offset := 0
		if req.Offset != nil && *req.Offset > 0 {
			offset = int(*req.Offset)
		}
		result, err := ws.List(req.Path, workspace.FSListOptions{
			Depth: req.Depth, IncludeHidden: includeHidden, Limit: req.Limit, Offset: offset,
			FollowSymlinks: followSymlinks, RespectGitIgnore: respectGitIgnore,
			IncludeGlobs: req.IncludeGlobs, ExcludeGlobs: req.ExcludeGlobs,
		})
		extResult(result, err)
	case "x.ai/fs/exists":
		extResult(map[string]any{"exists": ws.Exists(req.Path)}, nil)
	case "x.ai/fs/read_file":
		ranged := req.Offset != nil || req.Length != nil || req.Encoding == "base64"
		length := ^uint64(0)
		if req.Length != nil {
			length = *req.Length
		}
		offset := uint64(0)
		if req.Offset != nil {
			if *req.Offset < 0 {
				s.respondError(incoming.ID, -32602, "offset must not be negative")
				return
			}
			offset = uint64(*req.Offset)
		}
		result, err := ws.Read(req.Path, offset, length, req.MaxBytes, req.Encoding, ranged)
		if err == nil && req.MaxLines != nil && result.LineCount != nil && *result.LineCount > *req.MaxLines {
			err = fmt.Errorf("file exceeds %d lines", *req.MaxLines)
		}
		extResult(result, err)
	case "x.ai/fs/write_file":
		createDirs := true
		if req.CreateDirs != nil {
			createDirs = *req.CreateDirs
		}
		extResult(map[string]any{}, ws.Write(req.Path, req.Content, createDirs))
	case "x.ai/fs/delete_file":
		extResult(map[string]any{}, ws.Delete(req.Path))
	}
}

func sessionContextWire(used, total, turns int) map[string]any {
	free, percent := 0, 0
	if total > 0 {
		free = total - used
		if free < 0 {
			free = 0
		}
		percent = used * 100 / total
	}
	return map[string]any{
		"used": used, "total": total, "systemPromptTokens": 0,
		"toolDefinitionsCount": 0, "toolDefinitionsTokens": 0, "compactionCount": 0,
		"turnCount": turns, "toolCallCount": 0, "messageCount": 0, "messageTokens": 0,
		"freeTokens": free, "usagePct": percent, "autoCompactThresholdPercent": 85,
	}
}

func (s *Server) handleGit(ctx context.Context, incoming message) {
	if incoming.Method == "x.ai/git/serialize_changes" {
		s.respond(incoming.ID, map[string]any{"result": nil, "error": "git serialize_changes is unavailable in this build"})
		return
	}
	if incoming.Method == "x.ai/git/git_repo_root" {
		var req struct {
			CWD string `json:"currentWorkingDirectory"`
		}
		if json.Unmarshal(incoming.Params, &req) != nil || req.CWD == "" {
			s.respondError(incoming.ID, -32602, "currentWorkingDirectory is required")
			return
		}
		root, err := worktrees.GitRoot(ctx, req.CWD)
		if err != nil {
			s.respond(incoming.ID, "NotGitRepo")
			return
		}
		s.respond(incoming.ID, map[string]any{"GitRepo": map[string]any{"gitRoot": root}})
		return
	}
	var req struct {
		SessionID        string   `json:"sessionId"`
		GitRoot          string   `json:"gitRoot"`
		Paths            []string `json:"paths"`
		IncludeUntracked *bool    `json:"includeUntracked"`
		IncludeStats     bool     `json:"includeStats"`
		IgnoreSubmodules *bool    `json:"ignoreSubmodules"`
		IncludePatches   bool     `json:"includePatches"`
		Scope            string   `json:"scope"`
		Branch           string   `json:"branch"`
		Create           bool     `json:"create"`
		Commit           string   `json:"commit"`
		StashIfDirty     bool     `json:"stashIfDirty"`
		Message          string   `json:"message"`
		Amend            bool     `json:"amend"`
		Signoff          bool     `json:"signoff"`
		Push             bool     `json:"push"`
		Sync             bool     `json:"sync"`
		Path             string   `json:"path"`
		Content          string   `json:"content"`
		Version          string   `json:"version"`
		From             string   `json:"from"`
		To               string   `json:"to"`
		IncludePatch     bool     `json:"includePatch"`
		IncludeContent   bool     `json:"includeContent"`
		MergeBase        bool     `json:"mergeBase"`
		MaxPatchBytes    *uint64  `json:"maxPatchBytes"`
		MaxPatchLines    *uint64  `json:"maxPatchLines"`
	}
	if json.Unmarshal(incoming.Params, &req) != nil {
		s.respondError(incoming.ID, -32602, "invalid git parameters")
		return
	}
	if incoming.Method == "x.ai/git/checkout_session_head" {
		if req.SessionID == "" {
			s.respondError(incoming.ID, -32602, "sessionId is required")
			return
		}
		persisted, err := sessionlog.InfoByID(s.SessionDir, req.SessionID)
		if err != nil {
			s.respondError(incoming.ID, -32602, "session not found: "+req.SessionID)
			return
		}
		if persisted.HeadCommit == "" {
			s.respondError(incoming.ID, -32602, "session "+req.SessionID+" has no persisted HEAD commit")
			return
		}
		root := req.GitRoot
		if root == "" {
			root = persisted.CWD
		}
		root, err = worktrees.ValidateGitRoot(ctx, root)
		if err != nil {
			s.respondError(incoming.ID, -32602, err.Error())
			return
		}
		s.respond(incoming.ID, worktrees.CheckoutCommit(ctx, root, persisted.HeadCommit, req.StashIfDirty))
		return
	}
	root := req.GitRoot
	if root == "" && req.SessionID != "" {
		if session := s.lookupSession(req.SessionID); session != nil {
			root = session.cwd
		}
	}
	root, err := worktrees.ValidateGitRoot(ctx, root)
	if err != nil {
		s.respondError(incoming.ID, -32602, err.Error())
		return
	}
	extResult := func(value any, err error) {
		if err != nil {
			s.respond(incoming.ID, map[string]any{"result": nil, "error": err.Error()})
		} else {
			s.respond(incoming.ID, map[string]any{"result": value})
		}
	}
	if worktrees.IsJujutsu(root) {
		switch incoming.Method {
		case "x.ai/git/status":
			extResult(worktrees.JJStatus(ctx, root), nil)
			return
		case "x.ai/git/info":
			result, err := worktrees.JJInfo(ctx, root)
			extResult(result, err)
			return
		case "x.ai/git/current_commit":
			extResult(worktrees.JJCurrentCommit(ctx, root), nil)
			return
		case "x.ai/git/branches":
			result, err := worktrees.JJBranches(ctx, root)
			extResult(result, err)
			return
		case "x.ai/git/stage":
			extResult(map[string]any{"paths": []string{}}, nil)
			return
		case "x.ai/git/stage/content", "x.ai/git/unstage":
			extResult(map[string]any{}, nil)
			return
		case "x.ai/git/discard":
			extResult(map[string]any{}, worktrees.JJDiscard(ctx, root, req.Paths))
			return
		case "x.ai/git/commit":
			result, err := worktrees.JJCommit(ctx, root, req.Message)
			extResult(result, err)
			return
		case "x.ai/git/checkout":
			s.respondError(incoming.ID, -32602, "checkout is not supported in jj repos; use `jj new` or `jj edit`")
			return
		case "x.ai/git/stash":
			s.respondError(incoming.ID, -32602, "stash is not supported in jj repos; changes are always committed")
			return
		}
	}
	switch incoming.Method {
	case "x.ai/git/status":
		includeUntracked := true
		if req.IncludeUntracked != nil {
			includeUntracked = *req.IncludeUntracked
		}
		ignoreSubmodules := true
		if req.IgnoreSubmodules != nil {
			ignoreSubmodules = *req.IgnoreSubmodules
		}
		result, err := worktrees.Status(ctx, root, includeUntracked, req.IncludeStats, ignoreSubmodules, req.IncludePatches)
		extResult(result, err)
	case "x.ai/git/stage":
		paths, err := worktrees.Stage(ctx, root, req.Paths)
		extResult(map[string]any{"paths": paths}, err)
	case "x.ai/git/unstage":
		err := worktrees.Unstage(ctx, root, req.Paths)
		extResult(map[string]any{}, err)
	case "x.ai/git/discard":
		includeUntracked := req.IncludeUntracked != nil && *req.IncludeUntracked
		err := worktrees.Discard(ctx, root, req.Paths, req.Scope, includeUntracked)
		extResult(map[string]any{}, err)
	case "x.ai/git/current_commit":
		commit, err := worktrees.CurrentCommit(ctx, root)
		if err != nil {
			extResult(nil, err)
		} else {
			extResult(commit, nil)
		}
	case "x.ai/git/info":
		result, err := worktrees.Info(ctx, root)
		extResult(result, err)
	case "x.ai/git/branches":
		result, err := worktrees.Branches(ctx, root)
		extResult(result, err)
	case "x.ai/git/stash":
		includeUntracked := req.IncludeUntracked != nil && *req.IncludeUntracked
		extResult(map[string]any{}, worktrees.Stash(ctx, root, includeUntracked))
	case "x.ai/git/checkout":
		extResult(map[string]any{}, worktrees.CheckoutBranch(ctx, root, req.Branch, req.Create))
	case "x.ai/git/checkout_commit":
		if req.Commit == "" {
			s.respondError(incoming.ID, -32602, "commit is required")
			return
		}
		s.respond(incoming.ID, worktrees.CheckoutCommit(ctx, root, req.Commit, req.StashIfDirty))
	case "x.ai/git/commit":
		data, warning, err := worktrees.Commit(ctx, root, req.Message, req.Amend, req.Signoff, req.Push, req.Sync)
		if err != nil {
			extResult(nil, err)
		} else if warning != "" {
			s.respond(incoming.ID, map[string]any{"result": data, "error": warning})
		} else {
			extResult(data, nil)
		}
	case "x.ai/git/files":
		result, err := worktrees.ReadFiles(ctx, root, req.Paths, req.Version)
		extResult(result, err)
	case "x.ai/git/stage/content":
		if req.Path == "" {
			s.respondError(incoming.ID, -32602, "path is required")
			return
		}
		extResult(map[string]any{}, worktrees.StageContent(ctx, root, req.Path, req.Content))
	case "x.ai/git/diffs":
		result, err := worktrees.Diffs(ctx, root, req.Paths, req.From, req.To, req.IncludePatch, req.IncludeContent, req.MergeBase)
		if err == nil {
			exceeded := make([]string, 0)
			for _, file := range result.Files {
				if (req.MaxPatchBytes != nil && file.PatchBytes != nil && *file.PatchBytes > *req.MaxPatchBytes) ||
					(req.MaxPatchLines != nil && file.PatchLines != nil && *file.PatchLines > *req.MaxPatchLines) {
					exceeded = append(exceeded, file.Path)
				}
			}
			if len(exceeded) > 0 {
				err = fmt.Errorf("Diff exceeds size limits for %d file(s): %s", len(exceeded), strings.Join(exceeded, ", "))
			}
		}
		extResult(result, err)
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
			if req.CopyIgnoredInBackground {
				s.startIgnoredCopy(req.SessionID, req.SourcePath, record.Path, req.IgnoredSkipPatterns)
			}
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
	params.Path = newPathRewriter(current.displayCWD, current.cwd).rewritePath(params.Path)
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
	params.Path = newPathRewriter(current.displayCWD, current.cwd).rewritePath(params.Path)
	switch incoming.Method {
	case "x.ai/hunk-tracker/get-hunks":
		if params.Path != "" {
			data, err := tracker.FileData(ctx, params.Path, params.Source)
			if err != nil {
				s.respondError(incoming.ID, -32000, err.Error())
				return
			}
			s.respond(incoming.ID, getHunksWire{
				Hunks:    hunkWires(data.Hunks, tracker, current.cwd, current.displayCWD, true),
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
		s.respond(incoming.ID, getHunksWire{Hunks: hunkWires(hunks, tracker, current.cwd, current.displayCWD, false)})
	case "x.ai/hunk-tracker/get-files":
		files, err := tracker.Files(ctx)
		if err != nil {
			s.respondError(incoming.ID, -32000, err.Error())
			return
		}
		for index := range files {
			files[index].Path = displayHunkPath(current.cwd, current.displayCWD, files[index].Path)
		}
		s.respond(incoming.ID, map[string]any{"files": files})
	case "x.ai/hunk-tracker/get-all-file-contents":
		files, err := tracker.AllFileContents(ctx)
		if err != nil {
			s.respondError(incoming.ID, -32000, err.Error())
			return
		}
		for index := range files {
			files[index].Path = displayHunkPath(current.cwd, current.displayCWD, files[index].Path)
		}
		s.respond(incoming.ID, map[string]any{"files": files})
	case "x.ai/hunk-tracker/get-summary":
		summary, err := tracker.Summary(ctx)
		if err != nil {
			s.respondError(incoming.ID, -32000, err.Error())
			return
		}
		s.respond(incoming.ID, hunkSummaryWire{
			Stats: summary.Stats, Turns: hunkTurnWires(summary.Turns, tracker, current.cwd, current.displayCWD),
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
		Meta       map[string]any   `json:"_meta"`
	}
	if json.Unmarshal(incoming.Params, &params) != nil || params.CWD == "" {
		s.respondError(incoming.ID, -32602, "cwd is required")
		return
	}
	id, model, err := sessionStartupOverrides(params.Meta)
	if err != nil {
		s.respondError(incoming.ID, -32602, err.Error())
		return
	}
	if id == "" {
		id = fmt.Sprintf("gork-%s-%d", time.Now().UTC().Format("20060102T150405.000000000Z"), s.nextSession.Add(1))
	}
	servers, err := parseMCPServers(params.MCPServers)
	if err != nil {
		s.respondError(incoming.ID, -32602, err.Error())
		return
	}
	yoloMode, autoMode := sessionPermissionModeOverrides(params.Meta)
	sessionConfig := SessionConfig{
		CWD: params.CWD, Model: model, SessionID: id, MCPServers: servers,
		MCPSDKServers: parseMCPSDKServers(params.Meta),
		DisplayCWD:    stringMeta(params.Meta, "x.ai/display_cwd"),
		YoloMode:      yoloMode, AutoMode: autoMode, ClientHooks: parseClientHooks(params.Meta),
	}
	mcpStarted := time.Now()
	created, err := s.startSession(ctx, id, sessionConfig, "")
	if err != nil {
		s.respondError(incoming.ID, -32000, err.Error())
		return
	}
	response := sessionStartResponse(created, "default")
	meta := response["_meta"].(map[string]any)
	clientCWD := params.CWD
	if created.displayCWD != "" {
		clientCWD = created.displayCWD
	}
	meta["currentWorkingDirectory"] = clientCWD
	gitRoot, gitErr := worktrees.GitRoot(ctx, params.CWD)
	meta["isGitRepo"] = gitErr == nil
	if gitErr == nil {
		meta["gitRoot"] = displayEquivalentPath(params.CWD, created.displayCWD, gitRoot)
	} else {
		meta["gitRoot"] = nil
	}
	s.respond(incoming.ID, response)
	if toolCount := countMCPTools(created.runner); toolCount > 0 || len(servers) > 0 {
		s.NotifyMCPInitialized(id, toolCount, uint64(time.Since(mcpStarted).Milliseconds()))
	}
	if len(mcpServerCatalog(created)) > 0 {
		s.NotifyMCPServersUpdated(id)
	}
	if s.initialized.Load() {
		s.ForceAnnouncements()
	}
	s.startFolderTrustPrompt(created)
}

func countMCPTools(runner *agent.Runner) int {
	if runner == nil || runner.Tools == nil {
		return 0
	}
	count := 0
	for _, registered := range runner.Tools.SnapshotTools() {
		if _, ok := registered.(callableMCPTool); ok {
			count++
		}
	}
	return count
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
	} else if mergeErr := current.rewind.MergeFrom(*req.Target); mergeErr != nil {
		s.respondError(incoming.ID, -32000, mergeErr.Error())
		return
	}
	if wantsConversation {
		result, rewindErr := sessionlog.Rewind(path, *req.Target)
		if rewindErr != nil {
			s.respondError(incoming.ID, -32000, rewindErr.Error())
			return
		}
		current.previous = result.PreviousResponseID
		current.runner.RestoreHistory(result.Messages)
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
	sessionConfig.MCPInitProgress = func(total, connected int) {
		s.NotifyMCPInitProgress(id, total, connected)
	}
	sessionConfig.MCPReverseCall = s.callMCPSDK
	approver := &serverApprover{server: s, sessionID: id}
	writer := &sessionTextWriter{server: s, sessionID: id}
	runner, closeRuntime, err := s.Factory(ctx, sessionConfig, approver, writer, io.Discard)
	if err != nil {
		return nil, err
	}
	if closeRuntime == nil {
		closeRuntime = func() {}
	}
	fallbackFrom := strings.TrimSpace(sessionConfig.Model)
	fellBack := fallbackFrom != "" && !modelRequestAvailable(runner, fallbackFrom, sessionConfig.ResumePath != "")
	blocked := !hasAllowedModel(runner) || (fellBack && sessionConfig.ResumePath != "" && !sameModelFamily(fallbackFrom, runner.ModelID))
	if fellBack && sessionConfig.ResumePath != "" && !blocked {
		previous = ""
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
	if err := runner.Tools.SetPlanMode(mode == "plan"); err != nil {
		closeRuntime()
		return nil, err
	}
	created := &session{
		id: id, ctx: ctx, cwd: sessionConfig.CWD, displayCWD: sessionConfig.DisplayCWD, title: sessionConfig.Title, updated: time.Now().UTC(), previous: previous,
		runner: runner, close: closeRuntime, promptIndex: promptIndex, activePrompt: -1, rewind: rewind, logPath: sessionPath, mode: mode,
		mcpServers: append([]MCPServer(nil), sessionConfig.MCPServers...), permissions: approver,
		gitHeadEnabled: s.clientGitHead,
	}
	if blocked {
		created.unavailableModel = fallbackFrom
		if created.unavailableModel == "" {
			created.unavailableModel = runner.ModelID
		}
	}
	runner.SessionID = id
	runner.SessionPath = sessionPath
	s.attachClientHooks(runner, sessionConfig.ClientHooks, sessionPath, sessionConfig.CWD, id)
	if sessionConfig.ResumePath != "" && previous == "" {
		messages, historyErr := sessionlog.TranscriptOrEmpty(sessionPath)
		if historyErr != nil {
			closeRuntime()
			return nil, historyErr
		}
		runner.RestoreHistory(messages)
	}
	if fellBack && sessionConfig.ResumePath != "" && !blocked && runner.Logger != nil {
		if err := runner.Logger.Append("session_model", map[string]any{"model_id": runner.ModelID, "reasoning_effort": runner.ReasoningEffort}); err != nil {
			closeRuntime()
			return nil, err
		}
	}
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
	if rewriter := newPathRewriter(created.cwd, created.displayCWD); rewriter != nil {
		s.pathRewriters.Store(id, rewriter)
	}
	startSource := "new"
	if sessionConfig.ResumePath != "" {
		startSource = "load"
	}
	runner.StartHooks(hooks.WithSessionStartSource(ctx, startSource))
	s.startFileNotifications(created)
	s.startGitHeadNotifications(created)
	s.notifyRosterUpsert(created, "")
	if fellBack {
		newModel, reason := runner.ModelID, fmt.Sprintf("Model %q is no longer available; using %q.", fallbackFrom, runner.ModelID)
		if blocked {
			newModel = ""
			reason = fmt.Sprintf("Model %q is no longer available. Please start a new session or select another model.", fallbackFrom)
		}
		s.notifyModelAutoSwitched(id, fallbackFrom, newModel, reason)
	}
	return created, nil
}

type promptRequest struct {
	SessionID string         `json:"sessionId"`
	Prompt    []promptBlock  `json:"prompt"`
	Meta      map[string]any `json:"_meta,omitempty"`
}

func (s *Server) handlePrompt(parent context.Context, incoming message) {
	var params promptRequest
	if err := json.Unmarshal(incoming.Params, &params); err != nil {
		s.respondError(incoming.ID, -32602, "invalid prompt parameters")
		return
	}
	current := s.lookupSession(params.SessionID)
	if current == nil {
		s.respondError(incoming.ID, -32602, "unknown session")
		return
	}
	s.handlePromptRequest(parent, incoming, current, params)
}

func (s *Server) handlePromptRequest(parent context.Context, incoming message, current *session, params promptRequest) {
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
	if err := s.applyPendingModel(current); err != nil {
		s.respondError(incoming.ID, -32000, err.Error())
		return
	}
	current.mu.Lock()
	unavailableModel := current.unavailableModel
	current.mu.Unlock()
	if unavailableModel != "" {
		s.notifyModelAutoSwitched(current.id, "", "", "Your previous model is no longer available. Please start a new session or select another model.")
		s.finishPrompt(incoming, current, newPromptLifecycle(params), "end_turn", agent.Result{}, nil, "")
		return
	}
	if s.queuePrompt(current, incoming, &params, prompt) {
		return
	}
	if enabled, ok := alwaysApproveCommand(prompt); ok {
		s.handleAlwaysApprovePrompt(incoming, current, newPromptLifecycle(params), enabled)
		s.markRunningPrompt(current, promptID(params.Meta))
		return
	}
	if command := sessionStatusCommand(prompt); command != "" {
		s.handleSessionStatusPrompt(incoming, current, newPromptLifecycle(params), command)
		s.markRunningPrompt(current, promptID(params.Meta))
		return
	}
	if result, ok := agent.ParsePrivacyCommand(prompt); ok {
		s.handleLocalMessagePrompt(incoming, current, newPromptLifecycle(params), result.Message)
		s.markRunningPrompt(current, promptID(params.Meta))
		return
	}
	if terminaldiag.IsCommand(prompt) {
		s.handleLocalMessagePrompt(incoming, current, newPromptLifecycle(params), terminaldiag.Report())
		s.markRunningPrompt(current, promptID(params.Meta))
		return
	}
	if action, path, ok := parseHookCommand(prompt); ok && current.runner != nil && current.runner.HookCatalog != nil {
		s.handleHookSlashPrompt(parent, incoming, current, newPromptLifecycle(params), action, path)
		s.markRunningPrompt(current, promptID(params.Meta))
		return
	}
	if command, ok := parsePluginCommand(prompt); ok && current.runner != nil && current.runner.PluginInventory != nil {
		s.handlePluginSlashPrompt(parent, incoming, current, newPromptLifecycle(params), command)
		s.markRunningPrompt(current, promptID(params.Meta))
		return
	}
	if text, ok := parseFeedbackCommand(prompt); ok && current.runner != nil && current.runner.SubmitFeedback != nil {
		s.handleFeedbackSlashPrompt(incoming, current, newPromptLifecycle(params), text)
		s.markRunningPrompt(current, promptID(params.Meta))
		return
	}
	if command, ok := parseGoalCommand(prompt); ok {
		var handled bool
		prompt, content, handled = s.handleLocalGoalPrompt(incoming, current, newPromptLifecycle(params), command)
		if handled {
			s.markRunningPrompt(current, promptID(params.Meta))
			return
		}
	}
	if strings.TrimSpace(prompt) == "/compact" {
		s.handleCompactPrompt(parent, incoming, current, newPromptLifecycle(params), "", false)
		s.markRunningPrompt(current, promptID(params.Meta))
		return
	}
	if strings.TrimSpace(prompt) == "/flush" {
		s.handleMemoryFlush(parent, incoming, current, false, newPromptLifecycle(params))
		s.markRunningPrompt(current, promptID(params.Meta))
		return
	}
	if strings.TrimSpace(prompt) == "/dream" {
		s.handleMemoryDream(parent, incoming, current, newPromptLifecycle(params))
		s.markRunningPrompt(current, promptID(params.Meta))
		return
	}
	if action, ok := tools.ParseMemoryCommand(prompt); ok {
		if action == "browse" {
			s.handleMemoryFiles(incoming, current, newPromptLifecycle(params))
		} else {
			s.handleMemoryToggle(parent, incoming, current, action == "enable", newPromptLifecycle(params))
		}
		s.markRunningPrompt(current, promptID(params.Meta))
		return
	}
	if expanded, ok := tools.ExpandLoopCommand(prompt); ok {
		prompt = expanded
		content = []api.ContentPart{{Type: "input_text", Text: expanded}}
	}
	current.mu.Lock()
	if current.closed {
		current.mu.Unlock()
		s.respondError(incoming.ID, -32000, "session is closed")
		return
	}
	if current.running {
		current.mu.Unlock()
		s.respondError(incoming.ID, -32000, "session already has an active prompt")
		return
	}
	runCtx, cancel := context.WithCancel(parent)
	runCtx = hooks.WithPromptID(runCtx, promptID(params.Meta))
	runDone := make(chan struct{})
	current.cancel = cancel
	current.running = true
	current.runDone = runDone
	current.runningPromptID = promptID(params.Meta)
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
		s.notifyRosterUpsert(current, "working")
		baseInstructions := current.runner.Instructions
		current.runner.Instructions = turnInstructionsForMode(baseInstructions, mode)
		result, err := current.runner.RunTurnParts(runCtx, prompt, content, previous)
		current.runner.Instructions = baseInstructions
		points, pointsErr := sessionlog.RewindPoints(current.logPath)
		stopReason := "end_turn"
		if errors.Is(runCtx.Err(), context.Canceled) {
			stopReason = "cancelled"
		}
		current.mu.Lock()
		if err == nil {
			current.previous = result.ResponseID
			current.inputTokens = result.InputTokens
		}
		if stopReason == "cancelled" {
			current.runner.ClearInterjections()
			current.interjectionQueue = nil
		} else {
			current.interjectionQueue = append(current.interjectionQueue, current.runner.TakeInterjections()...)
		}
		current.running = false
		current.runDone = nil
		current.activePrompt = -1
		if pointsErr == nil {
			current.promptIndex = len(points)
		}
		current.cancel = nil
		cancelTrigger := current.cancelTrigger
		current.cancelTrigger = ""
		current.updated = time.Now().UTC()
		close(runDone)
		current.mu.Unlock()
		s.notifyRosterUpsert(current, "idle")
		s.finishPrompt(incoming, current, newPromptLifecycle(params), stopReason, result, err, cancelTrigger)
		s.startNext(current)
	}()
	s.broadcastQueue(current)
}

func (s *Server) handleCompactPrompt(parent context.Context, incoming message, current *session, lifecycle promptLifecycle, userContext string, extension bool) {
	current.mu.Lock()
	if current.closed {
		current.mu.Unlock()
		s.failPrompt(incoming, current, lifecycle, "session is closed")
		return
	}
	if current.running {
		current.mu.Unlock()
		s.failPrompt(incoming, current, lifecycle, "session already has an active prompt")
		return
	}
	if current.previous == "" {
		current.mu.Unlock()
		s.failPrompt(incoming, current, lifecycle, "no completed response is available to compact")
		return
	}
	runCtx, cancel := context.WithCancel(parent)
	runDone := make(chan struct{})
	previous := current.previous
	current.cancel = cancel
	current.running = true
	current.runDone = runDone
	current.updated = time.Now().UTC()
	current.mu.Unlock()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.notifyRosterUpsert(current, "working")
		_, err := current.runner.CompactWithContext(runCtx, previous, userContext)
		stopReason := "end_turn"
		if errors.Is(runCtx.Err(), context.Canceled) {
			stopReason = "cancelled"
		}
		current.mu.Lock()
		if err == nil {
			current.previous = ""
		}
		current.running = false
		current.runDone = nil
		current.cancel = nil
		cancelTrigger := current.cancelTrigger
		current.cancelTrigger = ""
		current.updated = time.Now().UTC()
		close(runDone)
		current.mu.Unlock()
		s.notifyRosterUpsert(current, "idle")
		if err == nil {
			s.notifyXAI(current, map[string]any{
				"sessionUpdate": "auto_compact_completed", "tokens_after": 0, "summary_preview": nil,
			})
		}
		if extension {
			if err != nil {
				s.respondError(incoming.ID, -32000, err.Error())
			} else {
				s.respond(incoming.ID, map[string]any{})
			}
		} else {
			s.finishPrompt(incoming, current, lifecycle, stopReason, agent.Result{}, err, cancelTrigger)
		}
		s.startNext(current)
	}()
}

func (s *Server) handleMemoryExtension(parent context.Context, incoming message) {
	if incoming.Method == "x.ai/compact_conversation" {
		var params struct {
			SessionID      string `json:"sessionId"`
			SessionIDSnake string `json:"session_id"`
			UserContext    string `json:"userContext"`
			ContextSnake   string `json:"user_context"`
		}
		if json.Unmarshal(incoming.Params, &params) != nil {
			s.respondError(incoming.ID, -32602, "invalid compact parameters")
			return
		}
		if params.SessionID == "" {
			params.SessionID = params.SessionIDSnake
		}
		if params.UserContext == "" {
			params.UserContext = params.ContextSnake
		}
		current := s.lookupSession(params.SessionID)
		if current == nil {
			s.respondError(incoming.ID, -32602, "unknown session")
			return
		}
		s.handleCompactPrompt(parent, incoming, current, promptLifecycle{}, params.UserContext, true)
		return
	}
	if incoming.Method == "x.ai/memory/rewrite" {
		s.handleMemoryRewriteExtension(parent, incoming)
		return
	}
	var params struct {
		SessionID      string `json:"session_id"`
		SessionIDCamel string `json:"sessionId"`
	}
	if json.Unmarshal(incoming.Params, &params) != nil {
		s.respondError(incoming.ID, -32602, "invalid memory flush parameters")
		return
	}
	sessionID := params.SessionID
	if sessionID == "" {
		sessionID = params.SessionIDCamel
	}
	current := s.lookupSession(sessionID)
	if current == nil {
		s.respondError(incoming.ID, -32602, "unknown session")
		return
	}
	s.handleMemoryFlush(parent, incoming, current, true, promptLifecycle{})
}

func (s *Server) handleMemoryRewriteExtension(parent context.Context, incoming message) {
	var params struct {
		SessionID      string  `json:"sessionId"`
		RawText        *string `json:"rawText"`
		ContextSummary *string `json:"contextSummary"`
	}
	if json.Unmarshal(incoming.Params, &params) != nil || params.SessionID == "" || params.RawText == nil || params.ContextSummary == nil {
		s.respondError(incoming.ID, -32602, "sessionId, rawText and contextSummary are required")
		return
	}
	current := s.lookupSession(params.SessionID)
	if current == nil {
		s.respondError(incoming.ID, -32602, "unknown session")
		return
	}
	current.mu.Lock()
	if current.closed {
		current.mu.Unlock()
		s.respondError(incoming.ID, -32000, "session is closed")
		return
	}
	if current.running {
		current.mu.Unlock()
		s.respondError(incoming.ID, -32000, "session already has an active prompt")
		return
	}
	runCtx, cancel := context.WithCancel(parent)
	runDone := make(chan struct{})
	current.cancel, current.running, current.runDone, current.updated = cancel, true, runDone, time.Now().UTC()
	current.mu.Unlock()
	rawText, contextSummary := *params.RawText, *params.ContextSummary
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.notifyRosterUpsert(current, "working")
		rewritten, err := current.runner.RewriteMemoryNote(runCtx, rawText, contextSummary)
		current.mu.Lock()
		current.cancel, current.running, current.runDone, current.updated = nil, false, nil, time.Now().UTC()
		close(runDone)
		current.mu.Unlock()
		s.notifyRosterUpsert(current, "idle")
		if err != nil {
			s.respondError(incoming.ID, -32000, err.Error())
		} else {
			s.respond(incoming.ID, map[string]any{"rewritten": rewritten})
		}
		s.startNext(current)
	}()
}

func (s *Server) handleMemoryFiles(incoming message, current *session, lifecycle promptLifecycle) {
	current.mu.Lock()
	if current.closed {
		current.mu.Unlock()
		s.failPrompt(incoming, current, lifecycle, "session is closed")
		return
	}
	if current.running {
		current.mu.Unlock()
		s.failPrompt(incoming, current, lifecycle, "session already has an active prompt")
		return
	}
	current.mu.Unlock()
	files, err := current.runner.ListMemory()
	if err != nil {
		s.failPrompt(incoming, current, lifecycle, err.Error())
		return
	}
	s.notify(current.id, map[string]any{"sessionUpdate": "memory_files", "files": files})
	s.finishPrompt(incoming, current, lifecycle, "end_turn", agent.Result{}, nil, "")
}

func (s *Server) handleMemoryDream(parent context.Context, incoming message, current *session, lifecycle promptLifecycle) {
	current.mu.Lock()
	if current.closed {
		current.mu.Unlock()
		s.failPrompt(incoming, current, lifecycle, "session is closed")
		return
	}
	if current.running {
		current.mu.Unlock()
		s.failPrompt(incoming, current, lifecycle, "session already has an active prompt")
		return
	}
	runCtx, cancel := context.WithCancel(parent)
	runDone := make(chan struct{})
	current.cancel, current.running, current.runDone, current.updated = cancel, true, runDone, time.Now().UTC()
	current.mu.Unlock()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.notifyRosterUpsert(current, "working")
		result, err := current.runner.DreamMemory(runCtx, true)
		current.mu.Lock()
		current.cancel, current.running, current.runDone, current.updated = nil, false, nil, time.Now().UTC()
		cancelTrigger := current.cancelTrigger
		current.cancelTrigger = ""
		close(runDone)
		current.mu.Unlock()
		s.notifyRosterUpsert(current, "idle")
		update := map[string]any{"sessionUpdate": "memory_dream_completed", "result": result.Outcome}
		if result.Path != "" {
			update["path"] = result.Path
		}
		s.notify(current.id, update)
		stopReason := "end_turn"
		if errors.Is(runCtx.Err(), context.Canceled) {
			stopReason = "cancelled"
		}
		s.finishPrompt(incoming, current, lifecycle, stopReason, agent.Result{}, err, cancelTrigger)
		s.startNext(current)
	}()
}

func (s *Server) handleMemoryToggle(parent context.Context, incoming message, current *session, enabled bool, lifecycle promptLifecycle) {
	current.mu.Lock()
	if current.closed {
		current.mu.Unlock()
		s.failPrompt(incoming, current, lifecycle, "session is closed")
		return
	}
	if current.running {
		current.mu.Unlock()
		s.failPrompt(incoming, current, lifecycle, "session already has an active prompt")
		return
	}
	runCtx, cancel := context.WithCancel(parent)
	runDone := make(chan struct{})
	current.cancel, current.running, current.runDone, current.updated = cancel, true, runDone, time.Now().UTC()
	current.mu.Unlock()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.notifyRosterUpsert(current, "working")
		text, err := current.runner.SetMemoryEnabled(runCtx, enabled)
		current.mu.Lock()
		current.cancel, current.running, current.runDone, current.updated = nil, false, nil, time.Now().UTC()
		cancelTrigger := current.cancelTrigger
		current.cancelTrigger = ""
		close(runDone)
		current.mu.Unlock()
		s.notifyRosterUpsert(current, "idle")
		if err == nil {
			s.notify(current.id, map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": text}})
		}
		stopReason := "end_turn"
		if errors.Is(runCtx.Err(), context.Canceled) {
			stopReason = "cancelled"
		}
		s.finishPrompt(incoming, current, lifecycle, stopReason, agent.Result{}, err, cancelTrigger)
		s.startNext(current)
	}()
}

func (s *Server) handleMemoryFlush(parent context.Context, incoming message, current *session, extension bool, lifecycle promptLifecycle) {
	current.mu.Lock()
	if current.closed {
		current.mu.Unlock()
		s.failPrompt(incoming, current, lifecycle, "session is closed")
		return
	}
	if current.running {
		current.mu.Unlock()
		s.failPrompt(incoming, current, lifecycle, "session already has an active prompt")
		return
	}
	if current.previous == "" {
		current.mu.Unlock()
		s.failPrompt(incoming, current, lifecycle, "no completed response is available to flush")
		return
	}
	runCtx, cancel := context.WithCancel(parent)
	runDone := make(chan struct{})
	previous := current.previous
	current.cancel, current.running, current.runDone, current.updated = cancel, true, runDone, time.Now().UTC()
	current.mu.Unlock()
	s.notify(current.id, map[string]any{"sessionUpdate": "memory_flush_started"})
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.notifyRosterUpsert(current, "working")
		result, err := current.runner.FlushMemory(runCtx, previous)
		stopReason := "end_turn"
		if errors.Is(runCtx.Err(), context.Canceled) {
			stopReason = "cancelled"
		}
		current.mu.Lock()
		current.running, current.cancel, current.runDone, current.updated = false, nil, nil, time.Now().UTC()
		cancelTrigger := current.cancelTrigger
		current.cancelTrigger = ""
		close(runDone)
		current.mu.Unlock()
		s.notifyRosterUpsert(current, "idle")
		update := map[string]any{"sessionUpdate": "memory_flush_completed", "result": result.Outcome}
		if result.Path != "" {
			update["path"] = result.Path
		}
		if err != nil {
			update["result"] = "error: " + err.Error()
		}
		s.notify(current.id, update)
		if extension {
			if err != nil && stopReason != "cancelled" {
				s.respondError(incoming.ID, -32000, err.Error())
			} else {
				s.respond(incoming.ID, map[string]any{})
			}
		} else {
			s.finishPrompt(incoming, current, lifecycle, stopReason, agent.Result{}, err, cancelTrigger)
		}
		s.startNext(current)
	}()
}

func (s *Server) handleRestoreSession(ctx context.Context, incoming message, replay bool) {
	var params struct {
		SessionID  string           `json:"sessionId"`
		CWD        string           `json:"cwd"`
		MCPServers []mcpServerParam `json:"mcpServers"`
		Meta       map[string]any   `json:"_meta"`
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
	model, title := "", ""
	displayCWD := ""
	reasoningEffort := ""
	sessionHead, sessionBranch := "", ""
	for _, item := range items {
		if item.SessionID == params.SessionID {
			found = true
			model = item.ModelID
			displayCWD = item.DisplayCWD
			title = item.Title
			reasoningEffort = item.ReasoningEffort
			sessionHead = item.HeadCommit
			sessionBranch = item.HeadBranch
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
	noReplay, _ := params.Meta["noReplay"].(bool)
	restoreCode, _ := params.Meta["x.ai/restore_code"].(bool)
	if requested := stringMeta(params.Meta, "x.ai/display_cwd"); requested != "" {
		displayCWD = requested
	}
	var codeRestore map[string]any
	if restoreCode && sessionHead != "" {
		codeRestore = restoreSessionCode(ctx, params.CWD, sessionHead)
	}
	if replay && !noReplay {
		if err := s.replaySessionWithPaths(path, params.SessionID, params.CWD, displayCWD); err != nil {
			s.respondError(incoming.ID, -32000, err.Error())
			return
		}
	}
	yoloMode, autoMode := sessionPermissionModeOverrides(params.Meta)
	config := SessionConfig{
		CWD: params.CWD, Title: title, Model: model, ReasoningEffort: reasoningEffort, SessionID: params.SessionID,
		DisplayCWD: displayCWD,
		ResumePath: path, MCPServers: servers, MCPSDKServers: parseMCPSDKServers(params.Meta), YoloMode: yoloMode, AutoMode: autoMode,
		ClientHooks: parseClientHooks(params.Meta),
	}
	created, err := s.startSession(ctx, params.SessionID, config, previous)
	if err != nil {
		s.respondError(incoming.ID, -32000, err.Error())
		return
	}
	current := s.lookupSession(params.SessionID)
	mode := "default"
	if current != nil {
		current.mu.Lock()
		mode = current.mode
		current.mu.Unlock()
	}
	response := sessionStartResponse(created, mode)
	meta := response["_meta"].(map[string]any)
	meta["sessionId"] = params.SessionID
	if persist, ok := params.Meta["x.ai/persist"]; ok {
		meta["x.ai/persist"] = persist
	}
	if currentHead, headErr := worktrees.Head(ctx, params.CWD); headErr == nil {
		if divergence := worktrees.DetectHeadDivergence(sessionHead, sessionBranch, currentHead); divergence != nil {
			meta["gitDivergence"] = divergence
		}
	}
	if codeRestore != nil {
		responseMeta := response["_meta"].(map[string]any)
		responseMeta["codeRestore"] = codeRestore
	}
	s.respond(incoming.ID, response)
	if len(mcpServerCatalog(created)) > 0 {
		s.NotifyMCPServersUpdated(params.SessionID)
	}
	s.startFolderTrustPrompt(created)
}

func restoreSessionCode(ctx context.Context, cwd, commit string) map[string]any {
	root, err := worktrees.ValidateGitRoot(ctx, cwd)
	if err != nil {
		return map[string]any{"restored": false, "summary": "restore aborted (checkout failed): " + err.Error(), "degree": nil}
	}
	outcome := worktrees.CheckoutCommit(ctx, root, commit, true)
	if outcome.CheckedOut {
		short := commit
		if len(short) > 7 {
			short = short[:7]
		}
		return map[string]any{"restored": true, "summary": "checked out " + short + " (session registry disabled — staged/unstaged/untracked not restored)", "degree": "head_only"}
	}
	summary := "restore aborted (checkout failed)"
	if outcome.Error != "" {
		summary += ": " + outcome.Error
	}
	return map[string]any{"restored": false, "summary": summary, "degree": nil}
}

func (s *Server) replaySession(path, sessionID string) error {
	return s.replaySessionWithPaths(path, sessionID, "", "")
}

func (s *Server) replaySessionWithPaths(path, sessionID, realCWD, displayCWD string) error {
	messages, err := sessionlog.Transcript(path)
	if err != nil {
		return err
	}
	for _, historical := range messages {
		updateType := "agent_message_chunk"
		if historical.Role == "user" {
			updateType = "user_message_chunk"
		}
		if historical.Role != "user" || len(historical.Content) == 0 {
			s.notifyWithPaths(sessionID, map[string]any{
				"sessionUpdate": updateType,
				"content":       map[string]any{"type": "text", "text": historical.Text},
			}, realCWD, displayCWD)
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
			s.notifyWithPaths(sessionID, map[string]any{"sessionUpdate": updateType, "content": content}, realCWD, displayCWD)
		}
	}
	events, err := sessionlog.Events(path, "subagent_spawned", "subagent_finished", "task_backgrounded", "task_completed", "xai_session_notification")
	if err != nil {
		return err
	}
	for _, event := range events {
		update, ok := event.Data.(map[string]any)
		if !ok {
			return fmt.Errorf("invalid %s event", event.Kind)
		}
		if rewritten, ok := newPathRewriter(realCWD, displayCWD).rewriteJSON(update).(map[string]any); ok {
			update = rewritten
		}
		switch event.Kind {
		case "subagent_spawned", "subagent_finished":
			s.notifySubagent(sessionID, update)
		case "task_backgrounded":
			s.write(map[string]any{"jsonrpc": "2.0", "method": "x.ai/task_backgrounded", "params": map[string]any{"sessionId": sessionID, "update": update}})
		case "task_completed":
			s.write(map[string]any{"jsonrpc": "2.0", "method": "x.ai/task_completed", "params": map[string]any{"sessionId": sessionID, "update": update}})
		case "xai_session_notification":
			s.write(map[string]any{"jsonrpc": "2.0", "method": "x.ai/session_notification", "params": update})
		}
	}
	return nil
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
	if currentMode(current) == params.ModeID {
		s.respond(incoming.ID, map[string]any{})
		return
	}
	if err := s.setSessionMode(current, params.ModeID); err != nil {
		s.respondError(incoming.ID, -32000, err.Error())
		return
	}
	s.respond(incoming.ID, map[string]any{})
}

func (s *Server) handleTogglePlanMode(raw json.RawMessage) {
	var params struct {
		SessionID string `json:"sessionId"`
	}
	if json.Unmarshal(raw, &params) != nil || params.SessionID == "" {
		return
	}
	current := s.lookupSession(params.SessionID)
	if current == nil {
		return
	}
	next := "plan"
	if currentMode(current) == "plan" {
		next = "default"
	}
	_ = s.setSessionMode(current, next)
}

func currentMode(current *session) string {
	current.mu.Lock()
	defer current.mu.Unlock()
	return current.mode
}

func (s *Server) setSessionMode(current *session, mode string) error {
	current.mu.Lock()
	if current.mode == mode {
		current.mu.Unlock()
		return nil
	}
	if err := current.runner.Tools.SetPlanMode(mode == "plan"); err != nil {
		current.mu.Unlock()
		return err
	}
	if current.runner.Logger != nil {
		if err := current.runner.Logger.Append("session_mode", map[string]any{"mode_id": mode}); err != nil {
			_ = current.runner.Tools.SetPlanMode(current.mode == "plan")
			current.mu.Unlock()
			return err
		}
	}
	current.mode = mode
	current.mu.Unlock()
	s.notify(current.id, map[string]any{"sessionUpdate": "current_mode_update", "currentModeId": mode})
	return nil
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

func turnInstructionsForMode(base, mode string) string {
	if mode == "plan" {
		return base
	}
	return instructionsForMode(base, mode)
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

func (s *Server) closeSession(id string) bool {
	s.mu.Lock()
	current := s.sessions[id]
	if current == nil {
		s.mu.Unlock()
		return false
	}
	current.mu.Lock()
	current.closed = true
	delete(s.sessions, id)
	current.mu.Unlock()
	s.mu.Unlock()
	s.notifyRosterRemoved(id)
	s.shutdownSession(current)
	s.pathRewriters.Delete(id)
	return true
}

func (s *Server) shutdownSession(current *session) {
	current.mu.Lock()
	current.wakeQueue = nil
	current.interjectionQueue = nil
	queued := current.promptQueue
	current.promptQueue = nil
	if current.runner != nil {
		current.runner.ClearInterjections()
	}
	if current.cancel != nil {
		current.cancel()
	}
	if current.btwCancel != nil {
		current.btwCancel()
	}
	if current.recapCancel != nil {
		current.recapCancel()
	}
	if current.suggestCancel != nil {
		current.suggestCancel()
	}
	if current.fileWatchCancel != nil {
		current.fileWatchCancel()
	}
	if current.gitWatchCancel != nil {
		current.gitWatchCancel()
	}
	runDone, btwDone, recapDone, suggestDone := current.runDone, current.btwDone, current.recapDone, current.suggestDone
	fileWatchDone, gitWatchDone := current.fileWatchDone, current.gitWatchDone
	current.mu.Unlock()
	for _, item := range queued {
		s.respondQueuedPromptCancelled(current, item)
	}
	if runDone != nil {
		<-runDone
	}
	if btwDone != nil {
		<-btwDone
	}
	if recapDone != nil {
		<-recapDone
	}
	if suggestDone != nil {
		<-suggestDone
	}
	if fileWatchDone != nil {
		<-fileWatchDone
	}
	if gitWatchDone != nil {
		<-gitWatchDone
	}
	if current.close != nil {
		current.close()
	}
	if s.terminals != nil {
		s.terminals.closeSessionCommands(current.id)
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
	current.wakeQueue = nil
	current.interjectionQueue = nil
	if current.runner != nil {
		current.runner.ClearInterjections()
	}
	if current.cancel != nil {
		if current.cancelTrigger == "" {
			current.cancelTrigger = "ctrl_c"
		}
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
	case "x.ai/terminal/create":
		var req struct {
			SessionID string   `json:"sessionId"`
			Command   *string  `json:"command"`
			Args      []string `json:"args"`
			CWD       string   `json:"cwd"`
			Env       []struct {
				Name  string `json:"name"`
				Value string `json:"value"`
			} `json:"env"`
			OutputByteLimit *int `json:"outputByteLimit"`
		}
		if json.Unmarshal(incoming.Params, &req) != nil || req.SessionID == "" || req.Command == nil || *req.Command == "" {
			s.respondError(incoming.ID, -32602, "sessionId and command are required")
			return
		}
		limit := terminalOutputBytes
		if req.OutputByteLimit != nil {
			limit = *req.OutputByteLimit
		}
		if limit < 0 {
			s.respondError(incoming.ID, -32602, "outputByteLimit must not be negative")
			return
		}
		env := make(map[string]string, len(req.Env))
		for _, item := range req.Env {
			env[item.Name] = item.Value
		}
		id, err := s.terminals.createCommand(req.SessionID, *req.Command, req.Args, req.CWD, env, limit)
		s.respondTerminal(incoming.ID, map[string]any{"terminalId": id}, err)
	case "x.ai/terminal/output":
		sessionID, terminalID, ok := parseTerminalIDRequest(incoming.Params)
		if !ok {
			s.respondError(incoming.ID, -32602, "sessionId and terminalId are required")
			return
		}
		result, err := s.terminals.commandOutput(sessionID, terminalID)
		s.respondTerminal(incoming.ID, result, err)
	case "x.ai/terminal/wait_for_exit":
		sessionID, terminalID, ok := parseTerminalIDRequest(incoming.Params)
		if !ok {
			s.respondError(incoming.ID, -32602, "sessionId and terminalId are required")
			return
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			result, err := s.terminals.waitCommand(sessionID, terminalID)
			if !s.closing.Load() {
				s.respondTerminal(incoming.ID, result, err)
			}
		}()
	case "x.ai/terminal/release", "x.ai/terminal/background":
		sessionID, terminalID, ok := parseTerminalIDRequest(incoming.Params)
		if !ok {
			s.respondError(incoming.ID, -32602, "sessionId and terminalId are required")
			return
		}
		var err error
		if incoming.Method == "x.ai/terminal/release" {
			err = s.terminals.releaseCommand(sessionID, terminalID)
		} else {
			err = s.terminals.backgroundCommand(sessionID, terminalID)
		}
		s.respondTerminal(incoming.ID, map[string]any{}, err)
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
		s.respondTerminal(incoming.ID, map[string]any{"terminalId": id}, err)
	case "x.ai/terminal/pty/load":
		var req struct {
			TerminalID string `json:"terminalId"`
		}
		if json.Unmarshal(incoming.Params, &req) != nil || req.TerminalID == "" {
			s.respondError(incoming.ID, -32602, "terminalId is required")
			return
		}
		result, err := s.terminals.load(req.TerminalID)
		s.respondTerminal(incoming.ID, result, err)
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
		s.respondTerminal(incoming.ID, map[string]any{}, s.terminals.resize(req.TerminalID, req.Rows, req.Cols))
	case "x.ai/terminal/list":
		s.respondTerminal(incoming.ID, map[string]any{"terminals": s.terminals.list()}, nil)
	case "x.ai/terminal/kill":
		var req struct {
			TerminalID string `json:"terminalId"`
			SessionID  string `json:"sessionId"`
		}
		if json.Unmarshal(incoming.Params, &req) != nil || req.TerminalID == "" {
			s.respondError(incoming.ID, -32602, "terminalId is required")
			return
		}
		var outcome string
		var err error
		if command := s.terminals.findCommand(req.TerminalID); command != nil {
			sessionID := req.SessionID
			if sessionID == "" {
				sessionID = command.sessionID
			}
			outcome, err = s.terminals.killCommand(sessionID, req.TerminalID)
		} else {
			outcome, err = s.terminals.kill(req.TerminalID)
		}
		s.respondTerminal(incoming.ID, map[string]any{"outcome": outcome}, err)
	}
}

func parseTerminalIDRequest(raw json.RawMessage) (string, string, bool) {
	var req struct {
		SessionID  string `json:"sessionId"`
		TerminalID string `json:"terminalId"`
	}
	if json.Unmarshal(raw, &req) != nil || req.SessionID == "" || req.TerminalID == "" {
		return "", "", false
	}
	return req.SessionID, req.TerminalID, true
}

func (s *Server) respondTerminal(id json.RawMessage, result any, err error) {
	if err != nil {
		s.respond(id, map[string]any{"result": nil, "error": err.Error()})
		return
	}
	s.respond(id, map[string]any{"result": result, "error": nil})
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
	if !s.closeSession(params.SessionID) {
		s.respondError(incoming.ID, -32602, "unknown session")
		return
	}
	s.respond(incoming.ID, map[string]any{})
}

func (s *Server) handleExtensionSessionClose(incoming message) {
	var params struct {
		SessionID string `json:"sessionId"`
	}
	if json.Unmarshal(incoming.Params, &params) != nil || params.SessionID == "" {
		s.respondError(incoming.ID, -32602, "sessionId is required")
		return
	}
	s.closeSession(params.SessionID)
	s.respond(incoming.ID, map[string]any{"result": map[string]any{"success": true}, "error": nil})
}

func (s *Server) lookupSession(id string) *session {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[id]
}

func (s *Server) closeAll() {
	s.closing.Store(true)
	if s.trustCancel != nil {
		s.trustCancel()
	}
	if s.worktrees != nil {
		s.worktrees.CancelCopies()
	}
	if s.terminals != nil {
		s.terminals.closeAll()
	}
	s.mu.Lock()
	fuzzySearch := s.fuzzySearch
	s.fuzzySearch = nil
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
	for _, pending := range s.pendingPlan {
		select {
		case pending <- planApprovalResult{err: io.EOF}:
		default:
		}
	}
	for _, pending := range s.pendingQuestion {
		select {
		case pending <- userQuestionResult{err: io.EOF}:
		default:
		}
	}
	for _, pending := range s.pendingTrust {
		select {
		case pending <- folderTrustResult{err: io.EOF}:
		default:
		}
	}
	for _, pending := range s.pendingMCP {
		select {
		case pending <- mcpReverseResult{err: io.EOF}:
		default:
		}
	}
	s.mu.Unlock()
	if fuzzySearch != nil {
		fuzzySearch.CloseAll()
	}
	for _, current := range sessions {
		current.mu.Lock()
		current.closed = true
		current.wakeQueue = nil
		queued := current.promptQueue
		current.promptQueue = nil
		if current.cancel != nil {
			current.cancel()
		}
		if current.btwCancel != nil {
			current.btwCancel()
		}
		if current.recapCancel != nil {
			current.recapCancel()
		}
		if current.suggestCancel != nil {
			current.suggestCancel()
		}
		if current.fileWatchCancel != nil {
			current.fileWatchCancel()
		}
		if current.gitWatchCancel != nil {
			current.gitWatchCancel()
		}
		runDone, btwDone, recapDone, suggestDone := current.runDone, current.btwDone, current.recapDone, current.suggestDone
		fileWatchDone, gitWatchDone := current.fileWatchDone, current.gitWatchDone
		current.mu.Unlock()
		for _, item := range queued {
			s.respondQueuedPromptCancelled(current, item)
		}
		if runDone != nil {
			<-runDone
		}
		if btwDone != nil {
			<-btwDone
		}
		if recapDone != nil {
			<-recapDone
		}
		if suggestDone != nil {
			<-suggestDone
		}
		if fileWatchDone != nil {
			<-fileWatchDone
		}
		if gitWatchDone != nil {
			<-gitWatchDone
		}
		current.close()
		s.pathRewriters.Delete(current.id)
	}
}

func (s *Server) startIgnoredCopy(sessionID, source, worktreePath string, patterns []string) {
	result := s.worktrees.StartIgnoredCopy(source, worktreePath, patterns)
	s.write(map[string]any{
		"jsonrpc": "2.0", "method": "x.ai/git/worktree/status",
		"params": map[string]any{
			"status": "copyingIgnored", "sessionId": sessionID, "worktreePath": worktreePath,
			"message": "Copying ignored files in background...",
		},
	})
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		outcome := <-result
		if s.closing.Load() {
			return
		}
		params := map[string]any{
			"status": "ignoredCopyComplete", "sessionId": sessionID, "worktreePath": worktreePath,
			"filesCopied": outcome.FilesCopied, "dirsCreated": outcome.DirsCreated,
		}
		if outcome.Err != nil {
			message := outcome.Err.Error()
			if outcome.Cancelled {
				message = "Background copy was cancelled"
			}
			params = map[string]any{
				"status": "ignoredCopyError", "sessionId": sessionID, "worktreePath": worktreePath,
				"message": message, "cancelled": outcome.Cancelled,
			}
		}
		s.write(map[string]any{"jsonrpc": "2.0", "method": "x.ai/git/worktree/status", "params": params})
	}()
}

func (s *Server) respond(id json.RawMessage, result any) {
	s.write(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func (s *Server) respondError(id json.RawMessage, code int, message string) {
	s.write(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": message}})
}

func (s *Server) respondErrorData(id json.RawMessage, code int, message string, data any) {
	s.write(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": message, "data": data}})
}

func (s *Server) notify(sessionID string, update any) {
	update = s.rewriteSessionValue(sessionID, update)
	s.write(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": sessionID, "update": update}})
}

func (s *Server) notifyWithPaths(sessionID string, update any, realCWD, displayCWD string) {
	update = newPathRewriter(realCWD, displayCWD).rewriteJSON(update)
	s.write(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": sessionID, "update": update}})
}

func (s *Server) notifyXAI(current *session, update map[string]any) {
	params := map[string]any{
		"sessionId": current.id, "update": update,
		"_meta": map[string]any{
			"eventId":          fmt.Sprintf("%s-%d", current.id, s.nextRequest.Add(1)),
			"agentTimestampMs": time.Now().UnixMilli(),
		},
	}
	if current.runner == nil || current.runner.Logger == nil || current.runner.Logger.Append("xai_session_notification", params) != nil {
		delete(params, "_meta")
	}
	s.write(map[string]any{"jsonrpc": "2.0", "method": "x.ai/session_notification", "params": newPathRewriter(current.cwd, current.displayCWD).rewriteJSON(params)})
}

func (s *Server) write(value any) {
	s.writeResult(value)
}

func (s *Server) writeResult(value any) bool {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return json.NewEncoder(s.output).Encode(value) == nil
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
	if toolErr == nil && gitChangingTool(call.Name) {
		o.server.notifyGitHead(o.server.lookupSession(o.sessionID))
	}
}

func acpToolKind(name string) string {
	switch name {
	case "read_file", "list_dir", "list_files", "get_task_output", "get_background_command_output", "lsp":
		return "read"
	case "grep", "search_files":
		return "search"
	case "write_file", "edit_file", "search_replace":
		return "edit"
	case "run_terminal_cmd", "shell", "monitor", "start_background_command", "kill_task", "kill_background_command":
		return "execute"
	case "todo_write", "update_goal", "enter_plan_mode", "exit_plan_mode", "ask_user_question":
		return "think"
	default:
		return "other"
	}
}

type serverApprover struct {
	server    *Server
	sessionID string
	mu        sync.RWMutex
	grants    map[permissionGrant]struct{}
}

type permissionGrant struct {
	action string
	detail string
}

func (a *serverApprover) Approve(ctx context.Context, action, detail string) error {
	grant := permissionGrant{action: action, detail: detail}
	a.mu.RLock()
	_, allowed := a.grants[grant]
	a.mu.RUnlock()
	if allowed {
		return nil
	}
	a.server.beginRosterInteraction(a.sessionID)
	defer a.server.endRosterInteraction(a.sessionID)
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
				map[string]any{"optionId": "allow_always", "name": "Always allow this request", "kind": "allow_always"},
				map[string]any{"optionId": "reject_once", "name": "Reject", "kind": "reject_once"},
			},
		},
	})
	select {
	case response := <-result:
		if response.err != nil {
			return response.err
		}
		if response.outcome == "selected" {
			switch response.optionID {
			case "allow_once":
				return nil
			case "allow_always":
				a.mu.Lock()
				if a.grants == nil {
					a.grants = make(map[permissionGrant]struct{})
				}
				a.grants[grant] = struct{}{}
				a.mu.Unlock()
				return nil
			}
		}
		return &tools.PermissionDeniedError{Action: action}
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (a *serverApprover) reset() {
	a.mu.Lock()
	clear(a.grants)
	a.mu.Unlock()
}

func (s *Server) handleClientResponse(incoming message) {
	key := strings.Trim(string(incoming.ID), "\"")
	s.mu.Lock()
	planPending := s.pendingPlan[key]
	questionPending := s.pendingQuestion[key]
	hookPending := s.pendingHook[key]
	trustPending := s.pendingTrust[key]
	mcpPending := s.pendingMCP[key]
	pending := s.pending[key]
	s.mu.Unlock()
	if mcpPending != nil {
		result := mcpReverseResult{result: append(json.RawMessage(nil), incoming.Result...)}
		if len(incoming.Error) > 0 && string(incoming.Error) != "null" {
			var response struct {
				Message string `json:"message"`
			}
			if json.Unmarshal(incoming.Error, &response) != nil || response.Message == "" {
				response.Message = "ACP MCP reverse request failed"
			}
			result.err = errors.New(response.Message)
		}
		mcpPending <- result
		return
	}
	if trustPending != nil {
		handleFolderTrustResponse(trustPending, incoming)
		return
	}
	if hookPending != nil {
		handleClientHookResponse(hookPending, incoming)
		return
	}
	if planPending != nil {
		if len(incoming.Error) > 0 && string(incoming.Error) != "null" {
			planPending <- planApprovalResult{err: errors.New("ACP plan approval request failed")}
			return
		}
		var response tools.PlanModeDecision
		if json.Unmarshal(incoming.Result, &response) != nil || response.Outcome == "" {
			planPending <- planApprovalResult{err: errors.New("invalid ACP plan approval response")}
			return
		}
		planPending <- planApprovalResult{decision: response}
		return
	}
	if questionPending != nil {
		if len(incoming.Error) > 0 && string(incoming.Error) != "null" {
			questionPending <- userQuestionResult{err: errors.New("ACP user question request failed")}
			return
		}
		response, err := decodeUserQuestionResponse(incoming.Result)
		questionPending <- userQuestionResult{response: response, err: err}
		return
	}
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
