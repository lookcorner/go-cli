package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/lookcorner/go-cli/internal/acp"
	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/agents"
	"github.com/lookcorner/go-cli/internal/api"
	"github.com/lookcorner/go-cli/internal/auth"
	"github.com/lookcorner/go-cli/internal/config"
	"github.com/lookcorner/go-cli/internal/hooks"
	"github.com/lookcorner/go-cli/internal/lsp"
	"github.com/lookcorner/go-cli/internal/marketplace"
	"github.com/lookcorner/go-cli/internal/mcp"
	"github.com/lookcorner/go-cli/internal/memory"
	"github.com/lookcorner/go-cli/internal/plugin"
	"github.com/lookcorner/go-cli/internal/session"
	"github.com/lookcorner/go-cli/internal/skills"
	"github.com/lookcorner/go-cli/internal/subagent"
	"github.com/lookcorner/go-cli/internal/tools"
	"github.com/lookcorner/go-cli/internal/tui"
	"github.com/lookcorner/go-cli/internal/version"
	"github.com/lookcorner/go-cli/internal/workspace"
	worktrees "github.com/lookcorner/go-cli/internal/worktree"
)

type options struct {
	configPath         string
	workspace          string
	model              string
	baseURL            string
	backend            string
	system             string
	approval           string
	sessionDir         string
	maxSteps           int
	timeout            time.Duration
	showVersion        bool
	interactive        bool
	previousID         string
	resume             string
	tui                bool
	goal               bool
	goalRuns           int
	acp                bool
	trust              bool
	experimentalMemory bool
	noMemory           bool
	allow              stringListFlag
	deny               stringListFlag
}

type stringListFlag []string

func (f *stringListFlag) String() string { return strings.Join(*f, ",") }
func (f *stringListFlag) Set(value string) error {
	*f = append(*f, value)
	return nil
}

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintln(os.Stderr, "gork:", err)
		os.Exit(1)
	}
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) > 0 && args[0] == "login" {
		return runLogin(args[1:], stdin, stdout, stderr)
	}
	if len(args) > 0 && args[0] == "logout" {
		return runLogout(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "setup" {
		return runSetup(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "plugin" {
		return runPlugin(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "memory" {
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		return runMemory(args[1:], cwd, stdin, stdout, stderr)
	}
	var opts options
	flags := flag.NewFlagSet("gork", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&opts.configPath, "config", "", "path to JSON config file")
	flags.StringVar(&opts.workspace, "workspace", ".", "workspace directory")
	flags.StringVar(&opts.model, "model", "", "model ID (or GORK_MODEL)")
	flags.StringVar(&opts.baseURL, "base-url", "", "Responses-compatible API base URL")
	flags.StringVar(&opts.backend, "backend", "", "model API backend: responses, chat_completions, or anthropic_messages")
	flags.StringVar(&opts.system, "system", "", "additional agent instructions")
	flags.StringVar(&opts.approval, "approval", "prompt", "write/shell approval: prompt, auto, always-approve, or deny")
	flags.Var(&opts.allow, "allow", "allow matching Tool(pattern) permission rule; repeatable")
	flags.Var(&opts.deny, "deny", "deny matching Tool(pattern) permission rule; repeatable")
	flags.StringVar(&opts.sessionDir, "session-dir", "", "session JSONL directory")
	flags.IntVar(&opts.maxSteps, "max-steps", 0, "maximum model/tool iterations")
	flags.DurationVar(&opts.timeout, "timeout", 0, "overall run timeout")
	flags.BoolVar(&opts.showVersion, "version", false, "print version")
	flags.BoolVar(&opts.interactive, "interactive", false, "start an interactive multi-turn session")
	flags.StringVar(&opts.previousID, "previous-response-id", "", "continue a stored Responses API conversation")
	flags.StringVar(&opts.resume, "resume", "", "resume a JSONL session path or 'latest'")
	flags.BoolVar(&opts.tui, "tui", false, "start the full-screen terminal interface")
	flags.BoolVar(&opts.goal, "goal", false, "keep running turns until update_goal completes or blocks the goal")
	flags.IntVar(&opts.goalRuns, "goal-runs", 10, "maximum turns in --goal mode")
	flags.BoolVar(&opts.acp, "acp", false, "serve Agent Client Protocol v1 over stdio")
	flags.BoolVar(&opts.trust, "trust", false, "trust this workspace's executable project configuration")
	flags.BoolVar(&opts.experimentalMemory, "experimental-memory", false, "enable cross-session workspace memory")
	flags.BoolVar(&opts.noMemory, "no-memory", false, "disable cross-session memory")
	flags.Usage = func() {
		fmt.Fprintf(stderr, "Usage: gork [flags] [prompt]\n       gork login [--oauth|--device-auth]\n       gork logout\n       gork setup\n       gork plugin <list|install|update|uninstall|marketplace>\n       gork memory clear [--workspace|--global|--all] [-y|--yes]\n\n")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		return err
	}
	if opts.showVersion {
		fmt.Fprintln(stdout, version.Current)
		return nil
	}
	if opts.tui && opts.interactive {
		return errors.New("--tui and --interactive are mutually exclusive")
	}
	if opts.goal && (opts.tui || opts.interactive) {
		return errors.New("--goal cannot be combined with --tui or --interactive")
	}
	if opts.acp && (opts.tui || opts.interactive || opts.goal) {
		return errors.New("--acp cannot be combined with --tui, --interactive, or --goal")
	}
	if opts.goalRuns < 1 {
		return errors.New("--goal-runs must be greater than zero")
	}

	cfg, err := config.Load(opts.configPath)
	if err != nil {
		return err
	}
	if err := prepareManagedPolicy(&cfg, opts.configPath, stderr); err != nil {
		return err
	}
	if opts.noMemory {
		cfg.OverrideMemory(false)
	} else if opts.experimentalMemory {
		cfg.OverrideMemory(true)
	}
	if opts.model != "" {
		cfg.Model = opts.model
		cfg.DefaultModelID = ""
		cfg.ReasoningEffort = ""
		cfg.ModelSupportsReasoningEffort = false
		cfg.ModelReasoningEfforts = nil
	}
	if opts.baseURL != "" {
		cfg.BaseURL = strings.TrimRight(opts.baseURL, "/")
	}
	if opts.backend != "" {
		cfg.Backend = opts.backend
	}
	if opts.system != "" {
		cfg.SystemPrompt = opts.system
	}
	if opts.maxSteps > 0 {
		cfg.MaxSteps = opts.maxSteps
	}
	if err := cfg.ValidateAuthPolicy(); err != nil {
		return err
	}
	if cfg.DisableAPIKeyAuth || cfg.ForceLoginTeamConfigured || cfg.PreferredAuthMethod == "oidc" {
		cfg.APIKey = ""
	}
	var tokenProvider api.TokenProvider
	if cfg.APIKey == "" && cfg.PreferredAuthMethod != "api_key" && isXAIBaseURL(cfg.BaseURL) {
		path, pathErr := auth.DefaultPath()
		if pathErr != nil {
			return pathErr
		}
		authClient := auth.NewClient(&http.Client{Timeout: cfg.HTTPTimeout})
		authConfig := auth.DefaultConfig()
		applyAuthPolicy(&authConfig, cfg)
		external := auth.ExternalProvider{
			Command: cfg.AuthProviderCommand, Path: path, Scope: authConfig.Scope(),
			TokenTTL: cfg.AuthTokenTTL, Stderr: stderr, AllowedTeams: authConfig.AllowedTeams,
		}
		resolveToken := func(ctx context.Context, rejectedToken string) (string, error) {
			token, err := authClient.ResolveRejected(ctx, path, authConfig, rejectedToken)
			if err == nil || external.Command == "" {
				return token, err
			}
			return external.Resolve(ctx, rejectedToken)
		}
		token, authErr := resolveToken(context.Background(), "")
		if authErr == nil {
			cfg.APIKey = token
			tokenProvider = resolveToken
		} else if !errors.Is(authErr, os.ErrNotExist) {
			return fmt.Errorf("load dynamic credentials: %w", authErr)
		}
	}
	if tokenProvider != nil {
		settingsCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		remote := config.FetchRemoteSettings(settingsCtx, cfg.ProxyBaseURL, cfg.APIKey, &http.Client{Timeout: 3 * time.Second})
		cancel()
		cfg.ApplyRemoteSettings(remote)
	}
	_ = marketplace.AutoRegisterOfficial(opts.configPath, cfg.OfficialMarketplaceAutoRegister)
	allowRules, askRules, denyRules, err := permissionRules(cfg.Permission, opts.allow, opts.deny)
	if err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	if opts.acp {
		if opts.trust {
			ws, openErr := workspace.Open(opts.workspace)
			if openErr != nil {
				return openErr
			}
			if err := workspace.GrantFolderTrust(context.Background(), ws.Root()); err != nil {
				return err
			}
		}
		return runACP(cfg, opts, allowRules, askRules, denyRules, tokenProvider, stdin, stdout, stderr)
	}

	inputReader := bufio.NewReader(stdin)
	prompt := strings.TrimSpace(strings.Join(flags.Args(), " "))
	resumingGoal := opts.goal && opts.resume != ""
	if prompt == "" && !opts.interactive && !opts.tui && !resumingGoal {
		data, err := io.ReadAll(io.LimitReader(inputReader, 4<<20))
		if err != nil {
			return fmt.Errorf("read prompt: %w", err)
		}
		prompt = strings.TrimSpace(string(data))
	}
	if prompt == "" && !opts.interactive && !opts.tui && !resumingGoal {
		flags.Usage()
		return errors.New("prompt is required as arguments or stdin")
	}

	ws, err := workspace.Open(opts.workspace)
	if err != nil {
		return err
	}
	instructionFiles, err := ws.LoadInstructions(cfg.Compat)
	if err != nil {
		return err
	}
	projectInstructions := workspace.FormatInstructions(instructionFiles)
	projectTrusted, err := resolveProjectTrust(context.Background(), ws.Root(), cfg, opts.trust, inputReader, stderr, terminalIO(stdin) && terminalIO(stderr))
	if err != nil {
		return err
	}
	workspaceSource := cfg
	cfg, skillCatalog, plugins, err := discoverWorkspace(ws.Root(), workspaceSource, projectTrusted)
	if err != nil {
		return err
	}
	cfg.SystemPrompt = joinInstructions(cfg.SystemPrompt, projectInstructions, skillCatalog.Summary())
	mode := tools.PermissionMode(opts.approval)
	if mode != tools.PermissionPrompt && mode != tools.PermissionAuto && mode != tools.PermissionAlwaysApprove && mode != tools.PermissionDeny {
		return fmt.Errorf("invalid --approval %q", opts.approval)
	}
	if cfg.DisableBypassPermissionsMode && mode == tools.PermissionAlwaysApprove {
		mode = tools.PermissionPrompt
	}
	if mode == tools.PermissionAuto && !cfg.AutoModeEnabled() {
		mode = tools.PermissionPrompt
	}
	if opts.resume != "" && opts.previousID != "" {
		return errors.New("--resume and --previous-response-id cannot be used together")
	}
	if opts.resume != "" && cfg.Backend != "responses" {
		return fmt.Errorf("--resume requires the responses backend; %s history is process-local", cfg.Backend)
	}
	var logger *session.Logger
	var resumedTranscript string
	if opts.resume != "" {
		resumePath := opts.resume
		if resumePath == "latest" {
			resumePath, err = session.Latest(opts.sessionDir)
			if err != nil {
				return err
			}
		}
		var messages []session.Message
		messages, err = session.Transcript(resumePath)
		if err == nil {
			resumedTranscript = session.FormatTranscript(messages)
			logger, opts.previousID, err = session.Resume(resumePath)
		}
	} else {
		logger, err = session.NewLogger(opts.sessionDir)
	}
	if err != nil {
		return err
	}
	if opts.resume == "" {
		if err := logger.Append("session_metadata", sessionMetadata(context.Background(), ws.Root(), cfg.Model, cfg.ReasoningEffort)); err != nil {
			return err
		}
	}
	defer logger.Close()
	memoryStore, err := openMemoryStore(cfg, ws.Root(), logger.ID())
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if opts.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.timeout)
		defer cancel()
	}
	skillCatalog.Watch(ctx, time.Second)

	client, err := newModelClient(cfg, tokenProvider)
	if err != nil {
		return err
	}
	permissionClassifier, err := newPermissionClassifierConfig(cfg, tokenProvider)
	if err != nil {
		return err
	}
	var approver tools.Approver
	var askApprover tools.Approver
	var tuiBridge *tui.Bridge
	var terminalLines *terminalInput
	var terminalPrompts *terminalPrompter
	statusOutput := stderr
	if opts.tui {
		tuiBridge = tui.NewBridgeWithAutoLock(ctx, mode, cfg.DisableBypassPermissionsMode)
		defer tuiBridge.Close()
		approver = tuiBridge
		askApprover = tui.PromptApprover(tuiBridge)
		statusOutput = tuiBridge.StatusWriter()
	} else {
		terminalLines = newTerminalInput(ctx, inputReader)
		terminalPrompts = &terminalPrompter{input: terminalLines, output: stderr}
		askApprover = terminalPrompts
	}
	permissionPrompts := &permissionPromptApprover{base: askApprover}
	askApprover = permissionPrompts
	if opts.tui {
		if mode == tools.PermissionPrompt {
			approver = permissionPrompts
		}
	} else {
		approver, err = tools.NewModeApproverWithLocks(mode, permissionPrompts, cfg.DisableBypassPermissionsMode, !cfg.AutoModeEnabled())
		if err != nil {
			return err
		}
	}
	approver, err = tools.NewPolicyApprover(approver, askApprover, allowRules, askRules, denyRules)
	if err != nil {
		return err
	}
	registry := tools.NewRegistry(ws, approver)
	if err := registry.ConfigureFileToolset(cfg.Toolset.FileToolset, cfg.Toolset.Hashline.Scheme, cfg.Toolset.Hashline.HashLen, cfg.Toolset.Hashline.ChunkSize); err != nil {
		_ = registry.Close()
		return err
	}
	if err := tools.RegisterMemoryTools(registry, memoryStore, cfg.Memory); err != nil {
		_ = registry.Close()
		return err
	}
	registry.ConfigureGoalRoles(goalRoleConfig(cfg, opts.goal))
	registry.ConfigureUserQuestions(cfg.AskUserQuestion.TimeoutEnabled, time.Duration(cfg.AskUserQuestion.TimeoutSeconds)*time.Second)
	artifactDir, err := session.ArtifactDir(logger.Path())
	if err != nil {
		return err
	}
	registry.ConfigureWebFetch(tools.WebFetchConfig{
		ArtifactDir: artifactDir, ContextWindow: cfg.ContextWindow,
		ProxyEndpoint: cfg.WebFetch.ProxyEndpoint, AllowedDomains: cfg.WebFetch.AllowedDomains,
		RestrictDomains: cfg.WebFetch.DomainsConfigured,
	})
	registry.SetWebFetchEnabled(cfg.WebFetch.Enabled)
	if err := registry.ConfigureHunkState(artifactDir); err != nil {
		_ = registry.Close()
		return err
	}
	if err := registry.ConfigureSchedulerState(artifactDir); err != nil {
		_ = registry.Close()
		return err
	}
	if err := registry.ConfigurePlanMode(artifactDir); err != nil {
		_ = registry.Close()
		return err
	}
	if err := registry.ConfigureGoalVerification(artifactDir); err != nil {
		_ = registry.Close()
		return err
	}
	registry.SetGoalObserver(&sessionGoalObserver{logger: logger})
	if search, enabled := cfg.WebSearchEndpoint(); enabled {
		if err := registry.Register(tools.NewWebSearchTool(search.BaseURL, search.APIKey, search.Model, &http.Client{Timeout: cfg.HTTPTimeout})); err != nil {
			return err
		}
	}
	readPolicy, err := tools.NewPolicyApprover(
		tools.PromptApprover{Mode: tools.PermissionAuto}, askApprover,
		allowRules, askRules, denyRules,
	)
	if err != nil {
		return err
	}
	registry.SetReadPolicy(readPolicy)
	defer registry.Close()
	if skillCatalog.Count() > 0 {
		if err := registry.Register(skillCatalog.Tool()); err != nil {
			return err
		}
		fmt.Fprintf(statusOutput, "[gork] discovered %d skill(s)\n", skillCatalog.Count())
	}
	mcpRuntime := newSessionMCPRuntime(ctx, cfg, ws.Root(), registry, approver, tokenProvider, statusOutput)
	if err := mcpRuntime.Update(ctx, nil); err != nil {
		return err
	}
	defer mcpRuntime.Close()
	watchMCPConfig(ctx, time.Second, func() ([]string, error) {
		return config.MCPWatchPaths(ws.Root(), opts.configPath, workspaceSource, plugins, projectTrusted), nil
	}, func() error {
		reloaded, err := config.Load(opts.configPath)
		if err != nil {
			return err
		}
		workspaceSource.MCPServers = reloaded.MCPServers
		workspaceSource.Compat = reloaded.Compat
		base := workspaceSource
		base.MCPServers = config.DiscoverMCPServers(ws.Root(), base, plugins, projectTrusted)
		return mcpRuntime.UpdateBase(ctx, base)
	}, statusOutput)
	lspManager, err := startLSPServers(ctx, cfg, ws, registry, statusOutput)
	if err != nil {
		return err
	}
	if lspManager != nil {
		defer lspManager.Close()
	}
	hookCatalog := hooks.Discover(hooks.Config{
		WorkspaceRoot: ws.Root(), Compat: cfg.Compat, ProjectTrusted: projectTrusted, Plugins: plugins,
	})
	hookRuntime := &hooks.Runtime{
		Catalog: hookCatalog, WorkspaceRoot: ws.Root(), SessionID: logger.ID(), Model: cfg.Model,
	}
	permissionPrompts.SetNotify(func() {
		hookRuntime.Notification(context.Background(), "permission_prompt", "Tool permission requested", "", "info")
	})
	var scheduledQueue *scheduledWakeQueue
	var schedulerObserver tools.SchedulerObserver
	var wakeSink localWakeSink
	if tuiBridge != nil {
		schedulerObserver = tuiBridge
		wakeSink = tuiBridge
	} else {
		scheduledQueue = newScheduledWakeQueue()
		schedulerObserver = scheduledQueue
		wakeSink = scheduledQueue
	}
	processObserver := &sessionProcessObserver{sessionID: logger.ID(), logger: logger, hooks: hookRuntime, autoWake: cfg.AutoWakeEnabled, scheduler: schedulerObserver, wake: wakeSink, planApprover: askApprover}
	registry.SetProcessObserver(processObserver)
	registry.SetSchedulerObserver(processObserver)
	registry.SetPlanModeObserver(processObserver)
	if tuiBridge != nil {
		registry.SetUserQuestionObserver(tuiBridge)
	} else {
		registry.SetUserQuestionObserver(terminalPrompts)
	}
	defer hookRuntime.SessionEnded(context.Background(), "closed")
	agentCatalog, agentErrors := agents.Discover(agents.Config{
		WorkspaceRoot: ws.Root(), ProjectTrusted: projectTrusted, Compat: cfg.Compat, Plugins: plugins,
	})
	for _, agentErr := range agentErrors {
		fmt.Fprintln(statusOutput, "[gork] agent definition:", agentErr)
	}
	resolveSubagentModel := func(slug string) (subagent.ModelRuntime, bool) {
		resolved, ok := cfg.ResolveModel(slug)
		return subagent.ModelRuntime{Profile: slug, Model: resolved.Model, ContextWindow: resolved.ContextWindow, CompactThresholdPercent: resolved.AutoCompactThresholdPercent}, ok
	}
	worktreeManager, err := worktrees.NewManager(opts.sessionDir)
	if err != nil {
		return err
	}
	subagents, err := subagent.New(subagent.Config{
		Context: ctx, Catalog: agentCatalog, Tools: registry, WorkspaceRoot: ws.Root(), ParentModel: cfg.Model,
		PermissionClassifier: permissionClassifier,
		ContextWindow:        cfg.ContextWindow, CompactThresholdPercent: cfg.AutoCompactThresholdPercent,
		TwoPassCompaction: cfg.TwoPassCompaction,
		ResolveModel:      resolveSubagentModel, AvailableModels: cfg.ModelSlugs(), Skills: skillCatalog,
		SkillConfig: workspaceSkillsConfig(cfg, plugins), Worktrees: worktreeManager,
		Observer:   &sessionSubagentObserver{sessionID: logger.ID(), logger: logger, autoWake: cfg.AutoWakeEnabled, wake: wakeSink},
		SessionDir: filepath.Dir(logger.Path()), ParentSessionID: logger.ID(),
		AutoWake: func(result tools.SubagentResult) bool {
			return cfg.AutoWakeEnabled && wakeSink != nil && wakeSink.QueueWake(result.ID, formatLocalSubagentWake(result))
		},
		CancelWake: func(id string) {
			if wakeSink != nil {
				wakeSink.CancelWake(id)
			}
		},
		DisablePermissionBypass: cfg.DisableBypassPermissionsMode,
		ParentMCPServers:        mcpRuntime.Configs(),
		StartMCPServers: func(childCtx context.Context, root string, childTools *tools.Registry, servers []mcp.ServerConfig) (func(), error) {
			return startSubagentMCPServers(childCtx, cfg, root, childTools, approver, tokenProvider, statusOutput, servers)
		},
		NewClient: func(model subagent.ModelRuntime) (agent.ResponseStreamer, error) {
			child := cfg
			if model.Profile != "" {
				child, _ = cfg.ResolveModel(model.Profile)
			} else {
				child.Model = model.Model
			}
			return newModelClient(child, tokenProvider)
		}, Hooks: hookCatalog,
	})
	if err != nil {
		return err
	}
	if err := registry.SetSubagentBackend(subagents); err != nil {
		subagents.Close()
		return err
	}
	defer subagents.Close()
	runner := &agent.Runner{
		Client: client, Tools: registry, Skills: skillCatalog, Logger: logger,
		HookCatalog: hookCatalog, HookPolicy: hookRuntime,
		ListSubagents: subagents.List, GetSubagent: subagents.Output, KillSubagent: subagents.Kill,
		ListTasks: registry.BackgroundTasks, KillTask: registry.KillBackgroundTask,
		SessionID: logger.ID(), SessionPath: logger.Path(),
		Model: cfg.Model, Instructions: cfg.SystemPrompt, MaxSteps: cfg.MaxSteps,
		PermissionClassifier: permissionClassifier,
		TextOutput:           stdout, StatusOutput: stderr,
		ContextWindow: cfg.ContextWindow, CompactThresholdPercent: cfg.AutoCompactThresholdPercent,
		TwoPassCompaction: cfg.TwoPassCompaction,
		Memory:            memoryStore, MemoryConfig: cfg.Memory,
		OpenMemory:       memoryStoreOpener(cfg.Memory, ws.Root(), logger.ID()),
		UpdateMCPServers: mcpRuntime.Update, MCPServers: mcpRuntime.Configs,
	}
	defer waitRunnerMemory(runner)
	if opts.tui {
		return tui.Run(ctx, runner, tuiBridge, prompt, opts.previousID, resumedTranscript, ws.Root(), cfg.Model, tui.UIOptions{
			Mode: cfg.UI.KeepTextSelection, WordSeparators: cfg.UI.WordSeparators, MouseReportingToggle: cfg.UI.MouseReportingToggle, VimMode: cfg.UI.VimMode,
			ScrollLines: cfg.UI.ScrollLines, InvertScroll: cfg.UI.InvertScroll,
		})
	}
	fmt.Fprintf(stderr, "[gork] workspace: %s\n[gork] session: %s\n", ws.Root(), displayPath(logger.Path()))
	if opts.interactive {
		if resumedTranscript != "" {
			fmt.Fprintln(stdout, resumedTranscript)
		}
		return interactiveLoop(ctx, runner, scheduledQueue, terminalLines, stdout, stderr, prompt, opts.previousID)
	}
	if opts.goal {
		snapshot := registry.GoalSnapshot()
		if opts.resume != "" && snapshot.Objective != "" && snapshot.Status != "completed" {
			objective, err := registry.ResumeGoal()
			if err != nil {
				return err
			}
			if prompt == "" {
				prompt = "Continue working toward the active goal:\n" + objective + "\nVerify the remaining work before claiming completion."
			} else {
				prompt = "Continue working toward the active goal:\n" + objective + "\n\nAdditional user direction:\n" + prompt
			}
		} else {
			var budget int64
			prompt, budget = parseGoalBudget(prompt)
			if err := registry.BeginGoalWithBudget(prompt, budget); err != nil {
				return err
			}
		}
		planPath, err := registry.RunGoalPlanner(ctx)
		if err != nil {
			return fmt.Errorf("goal planning paused: %w", err)
		}
		if snapshot, limited := registry.EnforceGoalBudget(); limited {
			fmt.Fprintf(stderr, "[gork] Goal token budget reached (%d of %d tokens) - goal stopped.\n", snapshot.TokensUsed, snapshot.TokenBudget)
			return nil
		}
		prompt = appendGoalPlanReminder(prompt, planPath)
		scratch, scratchReady := registry.GoalScratch()
		prompt = appendGoalScratchReminder(prompt, scratch, scratchReady)
		return goalLoop(ctx, runner, registry, stdout, stderr, prompt, opts.previousID, opts.goalRuns, cfg.Goal.VerifierCount, cfg.Goal.ClassifierMaxRuns, cfg.Goal.ReverifyAfter)
	}
	return runHeadless(ctx, runner, scheduledQueue, stdout, stderr, prompt, opts.previousID)
}

func runMemory(args []string, cwd string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] != "clear" {
		return errors.New("usage: gork memory clear [--workspace|--global|--all] [-y|--yes]")
	}
	flags := flag.NewFlagSet("gork memory clear", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var workspaceScope, globalScope, allScope, yes, shortYes bool
	flags.BoolVar(&workspaceScope, "workspace", false, "clear workspace-scoped memory")
	flags.BoolVar(&globalScope, "global", false, "clear global MEMORY.md")
	flags.BoolVar(&allScope, "all", false, "clear workspace and global memory")
	flags.BoolVar(&yes, "yes", false, "skip confirmation")
	flags.BoolVar(&shortYes, "y", false, "skip confirmation")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 || boolCount(workspaceScope, globalScope, allScope) > 1 {
		return errors.New("memory clear accepts one scope: --workspace, --global, or --all")
	}
	root, err := memory.DefaultRoot()
	if err != nil {
		return err
	}
	workspacePath, err := memory.WorkspacePath(root, cwd)
	if err != nil {
		return err
	}
	type target struct {
		label string
		path  string
		clear func() (bool, error)
	}
	targets := []target{{"workspace memory", workspacePath, func() (bool, error) { return memory.ClearWorkspace(root, cwd) }}}
	if globalScope {
		targets = []target{{"global MEMORY.md", memory.GlobalPath(root), func() (bool, error) { return memory.ClearGlobal(root) }}}
	} else if allScope {
		targets = append(targets, target{"global MEMORY.md", memory.GlobalPath(root), func() (bool, error) { return memory.ClearGlobal(root) }})
	}
	existing := make([]target, 0, len(targets))
	for _, item := range targets {
		if _, statErr := os.Lstat(item.path); statErr == nil {
			existing = append(existing, item)
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return statErr
		}
	}
	if len(existing) == 0 {
		fmt.Fprintln(stdout, "Nothing to clear — no memory files found.")
		return nil
	}
	fmt.Fprintln(stdout, "The following will be deleted:")
	for _, item := range existing {
		fmt.Fprintf(stdout, "  %s: %s\n", item.label, item.path)
	}
	if !yes && !shortYes {
		fmt.Fprint(stdout, "\nAre you sure? [y/N] ")
		answer, readErr := bufio.NewReader(stdin).ReadString('\n')
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return readErr
		}
		answer = strings.ToLower(strings.TrimSpace(answer))
		if answer != "y" && answer != "yes" {
			fmt.Fprintln(stdout, "Cancelled.")
			return nil
		}
	}
	cleared := false
	var failures []string
	for _, item := range targets {
		removed, clearErr := item.clear()
		if clearErr != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", item.label, clearErr))
			continue
		}
		if removed {
			cleared = true
			fmt.Fprintln(stdout, "  Cleared:", item.label)
		}
	}
	if len(failures) == 0 {
		fmt.Fprintln(stdout, "Memory cleared.")
		return nil
	}
	if cleared {
		fmt.Fprintln(stdout, "Memory partially cleared. Errors:")
	} else {
		fmt.Fprintln(stderr, "Failed to clear memory:")
	}
	for _, failure := range failures {
		fmt.Fprintln(stderr, " ", failure)
	}
	if !cleared {
		return errors.New("clear failed")
	}
	return nil
}

func boolCount(values ...bool) int {
	count := 0
	for _, value := range values {
		if value {
			count++
		}
	}
	return count
}

func runLogin(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	cfg := auth.DefaultConfig()
	var authFile, configPath, scopes string
	var oauth, deviceAuth, noBrowser bool
	flags := flag.NewFlagSet("gork login", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.BoolVar(&oauth, "oauth", false, "sign in with the browser OAuth flow")
	flags.BoolVar(&deviceAuth, "device-auth", false, "sign in with the OAuth device flow")
	flags.BoolVar(&noBrowser, "no-browser", false, "print the verification URL without opening a browser")
	flags.StringVar(&cfg.Issuer, "issuer", cfg.Issuer, "OAuth issuer")
	flags.StringVar(&cfg.ClientID, "client-id", cfg.ClientID, "OAuth client ID")
	flags.StringVar(&scopes, "scopes", strings.Join(cfg.Scopes, " "), "space-separated OAuth scopes")
	flags.StringVar(&cfg.Audience, "audience", cfg.Audience, "OIDC audience")
	flags.StringVar(&authFile, "auth-file", "", "credential store path")
	flags.StringVar(&configPath, "config", "", "path to config file")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if oauth && deviceAuth {
		return errors.New("--oauth and --device-auth are mutually exclusive")
	}
	if len(flags.Args()) > 0 {
		return fmt.Errorf("unexpected login arguments: %s", strings.Join(flags.Args(), " "))
	}
	appConfig, err := config.Load(configPath)
	if err != nil {
		return err
	}
	applyAuthPolicy(&cfg, appConfig)
	cfg.Issuer = strings.TrimRight(strings.TrimSpace(cfg.Issuer), "/")
	cfg.ClientID = strings.TrimSpace(cfg.ClientID)
	cfg.Scopes = strings.Fields(scopes)
	if cfg.Issuer == "" || cfg.ClientID == "" || len(cfg.Scopes) == 0 {
		return errors.New("OAuth issuer, client ID, and scopes are required")
	}
	if authFile == "" {
		var err error
		authFile, err = auth.DefaultPath()
		if err != nil {
			return err
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	client := auth.NewClient(&http.Client{Timeout: 30 * time.Second})
	var credential auth.Credential
	if !deviceAuth {
		login, err := client.StartBrowserLogin(ctx, cfg)
		if err != nil {
			return err
		}
		defer login.Close()
		fmt.Fprintf(stderr, "Open this URL to sign in:\n\n  %s\n\n", login.AuthorizationURL)
		if !noBrowser && !openBrowser(login.AuthorizationURL) {
			fmt.Fprint(stderr, "Could not open a browser automatically; open the URL above manually.\n\n")
		}
		var pastedInput io.Reader
		if isTerminalInput(stdin) {
			pastedInput = stdin
			fmt.Fprintln(stderr, "Paste the callback URL or authorization code here if it does not connect:")
		}
		loginCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		defer cancel()
		credential, err = login.Complete(loginCtx, pastedInput)
		if err != nil {
			return err
		}
	} else {
		var err error
		credential, err = completeDeviceLogin(ctx, client, cfg, noBrowser, stderr)
		if err != nil {
			return err
		}
	}
	credential = client.Enrich(ctx, appConfig.ProxyBaseURL, "", credential)
	if err := auth.Save(authFile, cfg.Scope(), credential); err != nil {
		return fmt.Errorf("save OAuth credentials: %w", err)
	}
	if _, _, err := syncManagedPolicy(ctx, appConfig, &credential, 2); err != nil {
		fmt.Fprintf(stderr, "Managed configuration was not updated: %v\n", err)
	}
	fmt.Fprintln(stdout, "Signed in")
	return nil
}

func runSetup(args []string, stdout, stderr io.Writer) error {
	var configPath string
	var jsonOutput bool
	flags := flag.NewFlagSet("gork setup", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&configPath, "config", "", "path to config file")
	flags.BoolVar(&jsonOutput, "json", false, "print the served configuration without installing it")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if len(flags.Args()) > 0 {
		return fmt.Errorf("unexpected setup arguments: %s", strings.Join(flags.Args(), " "))
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	credential := loadStoredCredential(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if jsonOutput {
		token, team, _, source := managedPolicyCredential(cfg, credential)
		report := config.SetupReport{}
		if token != "" {
			client := config.NewPolicyClient(&http.Client{Timeout: 15 * time.Second})
			var err error
			report, err = client.FetchReport(ctx, cfg.ManagedPolicyURL(), token, team)
			if err != nil {
				return err
			}
			report.Source = &source
		}
		return json.NewEncoder(stdout).Encode(report)
	}
	changed, attempted, err := syncManagedPolicy(ctx, cfg, credential, 5)
	if err != nil {
		return err
	}
	if !attempted {
		return errors.New("setup requires GROK_DEPLOYMENT_KEY or a team sign-in")
	}
	if changed {
		fmt.Fprintln(stdout, "Managed configuration updated")
	} else {
		fmt.Fprintln(stdout, "Managed configuration is up to date")
	}
	return nil
}

func runPlugin(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("plugin command is required: list, install, update, uninstall, or marketplace")
	}
	switch args[0] {
	case "list":
		if len(args) != 1 {
			return errors.New("plugin list does not accept arguments")
		}
		registry, err := plugin.LoadInstallRegistry()
		if err != nil {
			return err
		}
		var keys []string
		for key := range registry.Repos {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		if len(keys) == 0 {
			fmt.Fprintln(stdout, "No plugins installed.")
			return nil
		}
		for _, key := range keys {
			repo := registry.Repos[key]
			var names []string
			for name := range repo.Plugins {
				names = append(names, name)
			}
			sort.Strings(names)
			fmt.Fprintf(stdout, "%s\t%s\t%s\n", key, repo.Kind.Type, strings.Join(names, ", "))
		}
		return nil
	case "install":
		if len(args) != 2 {
			return errors.New("usage: gork plugin install <git-url-or-local-path>")
		}
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		outcome, err := plugin.Install(args[1], cwd)
		if err != nil {
			return err
		}
		if err := config.UpdatePlugins("", func(settings *config.PluginsConfig) {
			for _, name := range outcome.Plugins {
				if !slices.Contains(settings.Enabled, name) {
					settings.Enabled = append(settings.Enabled, name)
				}
				settings.Disabled = slices.DeleteFunc(settings.Disabled, func(value string) bool { return value == name })
			}
		}); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "Installed %d plugin(s) from %s: %s\n", len(outcome.Plugins), args[1], strings.Join(outcome.Plugins, ", "))
		return nil
	case "update":
		if len(args) > 2 {
			return errors.New("usage: gork plugin update [plugin-name]")
		}
		name := ""
		if len(args) == 2 {
			name = args[1]
		}
		outcomes, err := plugin.Update(name)
		if err != nil {
			return err
		}
		for _, outcome := range outcomes {
			fmt.Fprintf(stdout, "%s: %s\n", outcome.RepoKey, strings.ReplaceAll(outcome.Status, "_", " "))
		}
		return nil
	case "uninstall":
		flags := flag.NewFlagSet("gork plugin uninstall", flag.ContinueOnError)
		flags.SetOutput(stderr)
		confirmed := flags.Bool("confirm", false, "confirm removal of a repository containing multiple plugins")
		keepData := flags.Bool("keep-data", false, "preserve plugin data directories")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 1 {
			return errors.New("usage: gork plugin uninstall [--confirm] <plugin-name>")
		}
		outcome, err := plugin.Uninstall(flags.Arg(0), *confirmed, *keepData)
		if err != nil {
			return err
		}
		if err := config.UpdatePlugins("", func(settings *config.PluginsConfig) {
			for _, name := range outcome.Plugins {
				settings.Enabled = slices.DeleteFunc(settings.Enabled, func(value string) bool { return value == name })
				settings.Disabled = slices.DeleteFunc(settings.Disabled, func(value string) bool { return value == name })
			}
		}); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "Uninstalled %d plugin(s) from %s: %s\n", len(outcome.Plugins), outcome.RepoKey, strings.Join(outcome.Plugins, ", "))
		return nil
	case "marketplace":
		return runMarketplace(args[1:], stdout, stderr)
	default:
		return fmt.Errorf("unknown plugin command %q", args[0])
	}
}

func runMarketplace(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("marketplace command is required: list, add, remove, or update")
	}
	_ = marketplace.AutoRegisterOfficial("")
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	switch args[0] {
	case "list":
		flags := flag.NewFlagSet("gork plugin marketplace list", flag.ContinueOnError)
		flags.SetOutput(stderr)
		asJSON := flags.Bool("json", false, "emit machine-readable JSON")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return errors.New("usage: gork plugin marketplace list [--json]")
		}
		sources, err := marketplace.Sources("", cwd)
		if err != nil {
			return err
		}
		if *asJSON {
			items := make([]map[string]any, 0, len(sources))
			for _, source := range sources {
				detail := map[string]any{}
				kind := "local"
				if source.Git != "" {
					kind, detail["url"] = "git", source.Git
					if source.Branch != "" {
						detail["branch"] = source.Branch
					}
				} else {
					detail["path"] = source.Path
				}
				items = append(items, map[string]any{"name": source.Name, "kind": kind, "source": detail})
			}
			encoder := json.NewEncoder(stdout)
			encoder.SetIndent("", "  ")
			return encoder.Encode(items)
		}
		if len(sources) == 0 {
			fmt.Fprintln(stdout, "No marketplace sources configured.")
			return nil
		}
		for _, source := range sources {
			identity := source.Path
			if source.Git != "" {
				identity = source.Git
			}
			fmt.Fprintf(stdout, "  %s: %s\n", source.Name, identity)
		}
		return nil
	case "add", "remove":
		if len(args) != 2 {
			return fmt.Errorf("usage: gork plugin marketplace %s <git-url-or-local-path>", args[0])
		}
		actionType := "add_source"
		if args[0] == "remove" {
			actionType = "remove_source"
		}
		outcome, err := marketplace.Execute("", cwd, marketplace.Action{Type: actionType, SourceURLOrPath: args[1]})
		return printMarketplaceOutcome(stdout, outcome, err)
	case "update":
		if len(args) > 2 {
			return errors.New("usage: gork plugin marketplace update [source-name]")
		}
		identity := ""
		if len(args) == 2 {
			sources, err := marketplace.Sources("", cwd)
			if err != nil {
				return err
			}
			for _, source := range sources {
				if source.Name == args[1] {
					identity = source.Path
					if source.Git != "" {
						identity = source.Git
					}
					break
				}
			}
			if identity == "" {
				return fmt.Errorf("marketplace source %q not found", args[1])
			}
		}
		outcome, err := marketplace.Execute("", cwd, marketplace.Action{Type: "refresh", SourceURLOrPath: identity})
		return printMarketplaceOutcome(stdout, outcome, err)
	default:
		return fmt.Errorf("unknown marketplace command %q", args[0])
	}
}

func printMarketplaceOutcome(stdout io.Writer, outcome marketplace.Outcome, err error) error {
	if err != nil {
		return err
	}
	if outcome.Status != "success" {
		return errors.New(outcome.Message)
	}
	fmt.Fprintln(stdout, outcome.Message)
	return nil
}

func syncManagedPolicy(ctx context.Context, cfg config.Config, credential *auth.Credential, attempts int) (bool, bool, error) {
	token, team, fingerprint, _ := managedPolicyCredential(cfg, credential)
	if token == "" {
		return false, false, nil
	}
	home, err := config.PolicyHome()
	if err != nil {
		return false, true, err
	}
	client := config.NewPolicyClient(&http.Client{Timeout: 15 * time.Second})
	client.Attempts = attempts
	changed, err := client.Sync(ctx, home, cfg.ManagedPolicyURL(), token, team, fingerprint)
	return changed, true, err
}

func managedPolicyCredential(cfg config.Config, credential *auth.Credential) (token, team, fingerprint, source string) {
	if cfg.DeploymentKey != "" {
		return cfg.DeploymentKey, "", config.DeploymentKeyFingerprint(cfg.DeploymentKey), "deploymentKey"
	}
	if credential != nil && credential.TeamID != "" {
		return credential.Key, credential.TeamID, "", "teamOauth"
	}
	return "", "", "", ""
}

func prepareManagedPolicy(cfg *config.Config, configPath string, stderr io.Writer) error {
	home, err := config.PolicyHome()
	if err != nil {
		return err
	}
	credential := loadStoredCredential(*cfg)
	_, team, fingerprint, _ := managedPolicyCredential(*cfg, credential)
	if (cfg.DeploymentKey != "" || credential != nil) && config.ManagedPolicyHardStale(home, team, fingerprint) {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		changed, _, syncErr := syncManagedPolicy(ctx, *cfg, credential, 2)
		cancel()
		if syncErr != nil {
			fmt.Fprintf(stderr, "Managed configuration refresh failed: %v\n", syncErr)
		} else if changed {
			reloaded, err := config.Load(configPath)
			if err != nil {
				return err
			}
			*cfg = reloaded
		}
	}
	return verifyManagedPolicy(*cfg)
}

func verifyManagedPolicy(cfg config.Config) error {
	home, err := config.PolicyHome()
	if err != nil {
		return err
	}
	expected, fingerprint := "", ""
	if cfg.DeploymentKey != "" {
		fingerprint = config.DeploymentKeyFingerprint(cfg.DeploymentKey)
	} else if credential := loadStoredCredential(cfg); credential != nil {
		expected = credential.TeamID
	}
	if err := config.VerifyManagedPolicy(home, expected, fingerprint); err != nil {
		return fmt.Errorf("managed policy verification failed: %w", err)
	}
	return nil
}

func loadStoredCredential(cfg config.Config) *auth.Credential {
	path, err := auth.DefaultPath()
	if err != nil {
		return nil
	}
	authConfig := auth.DefaultConfig()
	applyAuthPolicy(&authConfig, cfg)
	credential, err := auth.Load(path, authConfig.Scope())
	if err != nil || credential.TeamID == "" {
		return nil
	}
	return &credential
}

func completeDeviceLogin(ctx context.Context, client *auth.Client, cfg auth.Config, noBrowser bool, stderr io.Writer) (auth.Credential, error) {
	code, err := client.RequestDeviceCode(ctx, cfg)
	if err != nil {
		return auth.Credential{}, err
	}
	displayURL := code.VerificationURIComplete
	if displayURL == "" {
		displayURL = code.VerificationURI
	}
	fmt.Fprintf(stderr, "Open this URL to sign in:\n\n  %s\n\n", displayURL)
	if !noBrowser && !openBrowser(displayURL) {
		fmt.Fprint(stderr, "Could not open a browser automatically; open the URL above manually.\n\n")
	}
	if code.VerificationURIComplete != "" {
		fmt.Fprintf(stderr, "Confirm this code in your browser:\n\n  %s\n", code.UserCode)
	} else {
		fmt.Fprintf(stderr, "Enter this code in your browser:\n\n  %s\n", code.UserCode)
	}
	fmt.Fprint(stderr, "\nOnly continue with a code you requested. Do not share it.\n\nWaiting for authorization...\n")
	credential, err := client.CompleteDeviceLogin(ctx, cfg, code)
	if err != nil {
		return auth.Credential{}, err
	}
	return credential, nil
}

func isTerminalInput(input io.Reader) bool {
	file, ok := input.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func applyAuthPolicy(target *auth.Config, source config.Config) {
	if source.AuthPrincipalType != "" {
		target.PrincipalType = source.AuthPrincipalType
	}
	if source.AuthPrincipalID != "" {
		target.PrincipalID = source.AuthPrincipalID
	}
	if source.ForceLoginTeamConfigured {
		target.AllowedTeams = append([]string{}, source.ForceLoginTeams...)
	}
}

func runLogout(args []string, stdout, stderr io.Writer) error {
	cfg := auth.DefaultConfig()
	var authFile, configPath string
	flags := flag.NewFlagSet("gork logout", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&cfg.Issuer, "issuer", cfg.Issuer, "OAuth issuer")
	flags.StringVar(&cfg.ClientID, "client-id", cfg.ClientID, "OAuth client ID")
	flags.StringVar(&authFile, "auth-file", "", "credential store path")
	flags.StringVar(&configPath, "config", "", "path to config file")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if len(flags.Args()) > 0 {
		return fmt.Errorf("unexpected logout arguments: %s", strings.Join(flags.Args(), " "))
	}
	cfg.Issuer = strings.TrimRight(strings.TrimSpace(cfg.Issuer), "/")
	cfg.ClientID = strings.TrimSpace(cfg.ClientID)
	if cfg.Issuer == "" || cfg.ClientID == "" {
		return errors.New("OAuth issuer and client ID are required")
	}
	if authFile == "" {
		var err error
		authFile, err = auth.DefaultPath()
		if err != nil {
			return err
		}
	}
	credential, _ := auth.Load(authFile, cfg.Scope())
	if err := auth.Remove(authFile, cfg.Scope()); err != nil {
		return fmt.Errorf("remove credentials: %w", err)
	}
	appConfig, configErr := config.Load(configPath)
	if configErr == nil && appConfig.DeploymentKey == "" && credential.TeamID != "" {
		if home, err := config.PolicyHome(); err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := config.ClearManagedPolicy(ctx, home); err != nil {
				return fmt.Errorf("clear managed policy: %w", err)
			}
		}
	}
	fmt.Fprintln(stdout, "Signed out")
	return nil
}

func isXAIBaseURL(raw string) bool {
	parsed, err := url.Parse(raw)
	return err == nil && strings.EqualFold(parsed.Hostname(), "api.x.ai")
}

func modelCacheIdentity(cfg config.Config, tokenProvider api.TokenProvider) (string, string) {
	authMethod, baseURL := "api_key", cfg.BaseURL
	if tokenProvider != nil {
		authMethod, baseURL = "session", cfg.ProxyBaseURL
	} else if cfg.DeploymentKey != "" {
		authMethod, baseURL = "deployment", cfg.ProxyBaseURL
	}
	return authMethod, strings.TrimRight(baseURL, "/") + "/models"
}

func fetchACPModelCache(ctx context.Context, cfg config.Config, authMethod, origin, authPath, scope string) (config.ModelCache, error) {
	token := cfg.APIKey
	if authMethod == "deployment" {
		token = cfg.DeploymentKey
	}
	credential := auth.Credential{}
	if authMethod == "session" {
		credential, _ = auth.Load(authPath, scope)
	}
	return config.FetchModelCache(ctx, config.ModelFetchRequest{
		AuthMethod: authMethod, Origin: origin, InferenceBaseURL: cfg.BaseURL, Token: token,
		TokenHeader: auth.DefaultTokenHeader, UserID: credential.UserID, Email: credential.Email,
		HTTP: &http.Client{Timeout: cfg.HTTPTimeout},
	})
}

func shouldClearACPModelCache(authMethod string, result auth.LogoutResult) bool {
	return authMethod == "session" && result.ClearedCurrent
}

func newModelClient(cfg config.Config, tokenProvider api.TokenProvider) (agent.ResponseStreamer, error) {
	httpClient := &http.Client{Timeout: cfg.HTTPTimeout}
	switch cfg.Backend {
	case "responses":
		client := api.NewClient(cfg.BaseURL, cfg.APIKey, httpClient)
		client.SetTokenProvider(tokenProvider)
		return client, nil
	case "chat_completions":
		client := api.NewChatClient(cfg.BaseURL, cfg.APIKey, httpClient)
		client.SetTokenProvider(tokenProvider)
		client.SetPruning(modelPruningConfig(cfg))
		return client, nil
	case "anthropic_messages":
		client := api.NewMessagesClient(cfg.BaseURL, cfg.APIKey, httpClient)
		client.SetTokenProvider(tokenProvider)
		client.SetPruning(modelPruningConfig(cfg))
		return client, nil
	default:
		return nil, fmt.Errorf("unsupported backend %q", cfg.Backend)
	}
}

func newPermissionClassifierConfig(cfg config.Config, tokenProvider api.TokenProvider) (agent.PermissionClassifierConfig, error) {
	result := agent.PermissionClassifierConfig{
		PromptType: cfg.AutoModePromptType(), ReasoningEffort: cfg.AutoMode.ReasoningEffort,
	}
	if cfg.AutoMode.ClassifierModel == "" {
		return result, nil
	}
	classifier, ok := cfg.ResolveModel(cfg.AutoMode.ClassifierModel)
	if !ok {
		return agent.PermissionClassifierConfig{}, fmt.Errorf("auto_mode classifier model %q is not defined", cfg.AutoMode.ClassifierModel)
	}
	client, err := newModelClient(classifier, tokenProvider)
	if err != nil {
		return agent.PermissionClassifierConfig{}, fmt.Errorf("create auto_mode classifier client: %w", err)
	}
	result.Client, result.Model = client, classifier.Model
	return result, nil
}

func modelPruningConfig(cfg config.Config) api.PruningConfig {
	return api.PruningConfig{
		Enabled: cfg.Pruning.Enabled, KeepLastNTurns: cfg.Pruning.KeepLastNTurns,
		SoftTrimThreshold: cfg.Pruning.SoftTrimThreshold, SoftTrimHead: cfg.Pruning.SoftTrimHead,
		SoftTrimTail: cfg.Pruning.SoftTrimTail, HardClearAgeTurns: cfg.Pruning.HardClearAgeTurns,
	}
}

func openMemoryStore(cfg config.Config, workspaceRoot, sessionID string) (*memory.Store, error) {
	if !cfg.Memory.Enabled {
		return nil, nil
	}
	return newMemoryStore(workspaceRoot, sessionID, cfg.Memory.GC.MaxAgeDays)
}

func newMemoryStore(workspaceRoot, sessionID string, gcMaxAgeDays uint64) (*memory.Store, error) {
	root, err := memory.DefaultRoot()
	if err != nil {
		return nil, err
	}
	store, err := memory.OpenWorkspace(root, workspaceRoot, sessionID)
	if err != nil {
		return nil, err
	}
	go func() { _, _ = store.GC(gcMaxAgeDays) }()
	return store, nil
}

func memoryStoreOpener(cfg memory.Config, workspaceRoot, sessionID string) func() (*memory.Store, error) {
	if !cfg.Enabled {
		return nil
	}
	return func() (*memory.Store, error) { return newMemoryStore(workspaceRoot, sessionID, cfg.GC.MaxAgeDays) }
}

func waitRunnerMemory(runner *agent.Runner) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = runner.CloseMemory(ctx)
}

type sessionPluginState struct {
	updateMu  sync.Mutex
	root      string
	trusted   bool
	catalog   *skills.Catalog
	mcp       *sessionMCPRuntime
	mcpSource config.Config
	updateLSP func(config.Config) error
	lspSource config.Config
	inventory []plugin.Plugin
	hooks     *hooks.Catalog
	hookRun   *hooks.Runtime
	hookCfg   hooks.Config
	agents    *agents.Catalog
	subagents *subagent.Manager
}

type sessionProcessObserver struct {
	server       *acp.Server
	sessionID    string
	logger       *session.Logger
	hooks        *hooks.Runtime
	autoWake     bool
	scheduler    tools.SchedulerObserver
	wake         localWakeSink
	planApprover tools.Approver
}

type sessionSubagentObserver struct {
	server    *acp.Server
	sessionID string
	logger    *session.Logger
	autoWake  bool
	wake      localWakeSink
}

type localWakeSink interface {
	TrackWake(string)
	QueueWake(string, string) bool
	CancelWake(string)
}

type sessionGoalObserver struct {
	server    *acp.Server
	sessionID string
	logger    *session.Logger
}

func (o *sessionGoalObserver) GoalEvent(event tools.GoalEvent) {
	if o != nil && o.logger != nil {
		_ = o.logger.Append(event.Kind, event.Data)
	}
	if o != nil && o.server != nil {
		o.server.NotifyGoalEvent(o.sessionID, event)
	}
}

type permissionPromptApprover struct {
	base   tools.Approver
	mu     sync.RWMutex
	notify func()
}

func (a *permissionPromptApprover) SetNotify(notify func()) {
	a.mu.Lock()
	a.notify = notify
	a.mu.Unlock()
}

func (a *permissionPromptApprover) Approve(ctx context.Context, action, detail string) error {
	a.mu.RLock()
	notify := a.notify
	a.mu.RUnlock()
	if notify != nil {
		notify()
	}
	return a.base.Approve(ctx, action, detail)
}

func (o *sessionProcessObserver) TaskBackgrounded(event tools.ProcessBackgrounded) {
	if o.logger != nil {
		_ = o.logger.Append("task_backgrounded", acp.TaskBackgroundedUpdate(event))
	}
	if o.server != nil {
		o.server.NotifyTaskBackgrounded(o.sessionID, event)
	}
	if o.server == nil && o.autoWake && o.wake != nil {
		o.wake.TrackWake(event.TaskID)
	}
}

func (o *sessionProcessObserver) TaskCompleted(snapshot tools.ProcessSnapshot) {
	willWake := false
	if o.server != nil {
		willWake = o.autoWake && !snapshot.BlockWaited && !snapshot.ExplicitlyKilled && o.server.QueueTaskWake(o.sessionID, snapshot)
	} else if o.wake != nil {
		if o.autoWake && !snapshot.BlockWaited && !snapshot.ExplicitlyKilled {
			willWake = o.wake.QueueWake(snapshot.TaskID, formatLocalTaskWake(snapshot))
		} else {
			o.wake.CancelWake(snapshot.TaskID)
		}
	}
	if o.logger != nil {
		_ = o.logger.Append("task_completed", acp.TaskCompletedUpdate(snapshot, willWake))
	}
	if o.server != nil {
		o.server.NotifyTaskCompleted(o.sessionID, snapshot, willWake)
	}
	if o.hooks != nil {
		o.hooks.Notification(context.Background(), "task_complete", "Background task completed: "+snapshot.TaskID, "", "info")
	}
}

func (o *sessionProcessObserver) MonitorEvent(event tools.MonitorEvent) {
	if o.server != nil {
		o.server.NotifyMonitorEvent(o.sessionID, event)
	}
}

func (o *sessionProcessObserver) ScheduledTaskCreated(event tools.ScheduledTaskCreated) {
	if o.logger != nil {
		_ = o.logger.Append("scheduled_task_created", acp.ScheduledTaskCreatedUpdate(event))
	}
	if o.server != nil {
		o.server.NotifyScheduledTaskCreated(o.sessionID, event)
	}
	if o.scheduler != nil {
		o.scheduler.ScheduledTaskCreated(event)
	}
}

func (o *sessionProcessObserver) ScheduledTaskFired(event tools.ScheduledTaskFired) {
	if o.server != nil {
		o.server.NotifyScheduledTaskFired(o.sessionID, event)
	}
	if o.scheduler != nil {
		o.scheduler.ScheduledTaskFired(event)
	}
}

func (o *sessionProcessObserver) ScheduledTaskRemoved(taskID string) {
	if o.logger != nil {
		_ = o.logger.Append("scheduled_task_deleted", acp.ScheduledTaskDeletedUpdate(taskID))
	}
	if o.server != nil {
		o.server.NotifyScheduledTaskRemoved(o.sessionID, taskID)
	}
	if o.scheduler != nil {
		o.scheduler.ScheduledTaskRemoved(taskID)
	}
}

func (o *sessionProcessObserver) PlanModeEntered(event tools.PlanModeEvent) {
	if o.logger != nil {
		_ = o.logger.Append("session_mode", map[string]any{"mode_id": "plan"})
	}
	if o.server != nil {
		o.server.NotifyPlanModeChanged(o.sessionID, "plan")
	}
	if observer, ok := o.planApprover.(interface{ PlanModeEntered(tools.PlanModeEvent) }); ok {
		observer.PlanModeEntered(event)
	}
}

func (o *sessionProcessObserver) ApprovePlanModeExit(ctx context.Context, event tools.PlanModeEvent) (tools.PlanModeDecision, error) {
	if o.server != nil {
		return o.server.RequestPlanModeExit(ctx, o.sessionID, event)
	}
	if reviewer, ok := o.planApprover.(interface {
		ApprovePlanModeExit(context.Context, tools.PlanModeEvent) (tools.PlanModeDecision, error)
	}); ok {
		return reviewer.ApprovePlanModeExit(ctx, event)
	}
	if o.planApprover != nil {
		if err := o.planApprover.Approve(ctx, "exit plan mode", event.PlanContent); err != nil {
			return tools.PlanModeDecision{}, err
		}
	}
	return tools.PlanModeDecision{Outcome: "approved"}, nil
}

func (o *sessionProcessObserver) PlanModeExited(event tools.PlanModeEvent) {
	if o.logger != nil {
		_ = o.logger.Append("session_mode", map[string]any{"mode_id": "default"})
	}
	if o.server != nil {
		o.server.NotifyPlanModeChanged(o.sessionID, "default")
	}
	if observer, ok := o.planApprover.(interface{ PlanModeExited(tools.PlanModeEvent) }); ok {
		observer.PlanModeExited(event)
	}
}

func (o *sessionProcessObserver) AskUserQuestion(ctx context.Context, request tools.UserQuestionRequest) (tools.UserQuestionResponse, error) {
	if o.server == nil {
		return tools.UserQuestionResponse{Outcome: "questions_sent"}, nil
	}
	return o.server.RequestUserQuestion(ctx, o.sessionID, request)
}

func (o *sessionProcessObserver) TaskConsumed(taskID string) {
	if o.server != nil {
		o.server.CancelTaskWake(o.sessionID, taskID)
	} else if o.wake != nil {
		o.wake.CancelWake(taskID)
	}
}

func (o *sessionSubagentObserver) SubagentStarted(_ context.Context, event subagent.Started) {
	if o.logger != nil {
		_ = o.logger.Append("subagent_spawned", acp.SubagentStartedUpdate(o.sessionID, event))
	}
	if o.server != nil {
		o.server.NotifySubagentStarted(o.sessionID, event)
	}
	if o.server == nil && o.autoWake && event.Background && o.wake != nil {
		o.wake.TrackWake(event.ID)
	}
}

func (o *sessionSubagentObserver) SubagentProgress(_ context.Context, result tools.SubagentResult) {
	if o.server != nil {
		o.server.NotifySubagentProgress(o.sessionID, result)
	}
}

func (o *sessionSubagentObserver) SubagentEnded(_ context.Context, result tools.SubagentResult) {
	if o.logger != nil {
		_ = o.logger.Append("subagent_finished", acp.SubagentFinishedUpdate(result))
	}
	if o.server != nil {
		o.server.NotifySubagentEnded(o.sessionID, result)
	} else if !result.WillWake && o.wake != nil {
		o.wake.CancelWake(result.ID)
	}
}

func formatLocalSubagentWake(result tools.SubagentResult) string {
	status := "successfully"
	if result.Status != "completed" {
		status = "with failure"
	}
	return fmt.Sprintf("<system-reminder>\nBackground subagent %q (%s: %q) completed %s.\nDuration: %.1fs | Tool calls: %d | Turns: %d\nUse get_task_output with task_ids [%q] to retrieve the full result.\n</system-reminder>", result.ID, result.Type, result.Description, status, float64(result.DurationMS)/1000, result.ToolCalls, result.Turns, result.ID)
}

func formatLocalTaskWake(snapshot tools.ProcessSnapshot) string {
	status := "successfully"
	if snapshot.ExitCode == nil || *snapshot.ExitCode != 0 {
		status = "with failure"
	}
	return fmt.Sprintf("<system-reminder>\nBackground task %q completed %s.\nCommand: %s\nUse get_task_output with task_ids [%q] to retrieve the full output.\n</system-reminder>", snapshot.TaskID, status, snapshot.Command, snapshot.TaskID)
}

func resolveACPSessionPermissionMode(defaultMode tools.PermissionMode, yoloMode, autoMode *bool, disableBypass, autoEnabled bool) tools.PermissionMode {
	if defaultMode == tools.PermissionDeny {
		return defaultMode
	}
	yolo := defaultMode == tools.PermissionAlwaysApprove
	if yoloMode != nil {
		yolo = *yoloMode
	}
	auto := defaultMode == tools.PermissionAuto && !yolo
	if autoMode != nil {
		auto = *autoMode
	}
	mode := tools.PermissionPrompt
	if yolo {
		mode = tools.PermissionAlwaysApprove
	} else if auto {
		mode = tools.PermissionAuto
	}
	if disableBypass && mode == tools.PermissionAlwaysApprove || !autoEnabled && mode == tools.PermissionAuto {
		return tools.PermissionPrompt
	}
	return mode
}

func acpModelOptions(cfg config.Config) []agent.ModelOption {
	seen := make(map[string]bool)
	options := make([]agent.ModelOption, 0, len(cfg.ModelProfiles)+1)
	for _, name := range cfg.ModelSlugs() {
		id, resolved, ok := cfg.ResolveModelEntry(name)
		if !ok || resolved.Model == "" {
			continue
		}
		profile := cfg.ModelProfiles[id]
		displayName := profile.Name
		if displayName == "" {
			displayName = resolved.Model
		}
		options = append(options, agent.ModelOption{
			ID: id, Model: resolved.Model, Name: displayName, Description: profile.Description,
			Hidden:        cfg.ModelSelectable(id, resolved.Model) && !cfg.ModelVisible(id, resolved.Model),
			Disallowed:    !cfg.ModelSelectable(id, resolved.Model),
			ContextWindow: resolved.ContextWindow, ReasoningEffort: profile.ReasoningEffort,
			SupportsReasoningEffort: profile.SupportsReasoningEffort, ReasoningEfforts: acpReasoningEffortOptions(profile.ReasoningEfforts),
		})
		seen[id] = true
	}
	currentID := acpSessionModelID(cfg, "")
	if currentID != "" && !seen[currentID] && cfg.ModelSelectable(currentID, cfg.Model) {
		options = append(options, agent.ModelOption{
			ID: currentID, Model: cfg.Model, Name: cfg.Model, Hidden: !cfg.ModelVisible(currentID, cfg.Model), ContextWindow: cfg.ContextWindow,
			ReasoningEffort: cfg.ReasoningEffort, SupportsReasoningEffort: cfg.ModelSupportsReasoningEffort,
			ReasoningEfforts: acpReasoningEffortOptions(cfg.ModelReasoningEfforts),
		})
	}
	return options
}

func acpReasoningEffortOptions(options []config.ReasoningEffortOption) []agent.ReasoningEffortOption {
	result := make([]agent.ReasoningEffortOption, 0, len(options))
	for _, option := range options {
		result = append(result, agent.ReasoningEffortOption{
			ID: option.ID, Value: option.Value, Label: option.Label,
			Description: option.Description, Default: option.Default,
		})
	}
	return result
}

func acpSessionModelID(cfg config.Config, requested string) string {
	id, _ := resolveACPSessionModelEntry(cfg, requested, false)
	return id
}

func resolveACPSessionModelEntry(cfg config.Config, requested string, visibleOnly bool) (string, config.Config) {
	if requested = strings.TrimSpace(requested); requested != "" {
		if id, resolved, ok := cfg.ResolveModelEntry(requested); ok {
			allowed := cfg.ModelSelectable(id, resolved.Model)
			if visibleOnly {
				allowed = cfg.ModelVisible(id, resolved.Model)
			}
			if allowed {
				return id, resolved
			}
		}
	}
	preferred := cfg.DefaultModelID
	if preferred == "" {
		preferred = cfg.Model
	}
	if id, resolved, ok := cfg.ResolveModelEntry(preferred); ok && cfg.ModelVisible(id, resolved.Model) {
		return id, resolved
	}
	for _, name := range cfg.ModelSlugs() {
		if id, resolved, ok := cfg.ResolveModelEntry(name); ok && cfg.ModelVisible(id, resolved.Model) {
			return id, resolved
		}
	}
	return cfg.Model, cfg
}

func preferredACPModelID(cfg config.Config) string {
	id, _ := resolveACPSessionModelEntry(cfg, "", false)
	return id
}

func modelCatalogChanged(previous, next config.Config) bool {
	type catalog struct {
		Model, Default, Effort string
		Profiles               map[string]config.ModelProfile
		Allowed, Hidden        []string
		Disabled               []string
		SupportsEffort         bool
		Efforts                []config.ReasoningEffortOption
	}
	view := func(cfg config.Config) catalog {
		return catalog{
			Model: cfg.Model, Default: cfg.DefaultModelID, Effort: cfg.ReasoningEffort,
			Profiles: cfg.ModelProfiles, Allowed: cfg.AllowedModels, Hidden: cfg.HiddenModels,
			Disabled: cfg.DisabledModels, SupportsEffort: cfg.ModelSupportsReasoningEffort,
			Efforts: cfg.ModelReasoningEfforts,
		}
	}
	return !reflect.DeepEqual(view(previous), view(next))
}

func runACP(cfg config.Config, opts options, allowRules, askRules, denyRules []string, tokenProvider api.TokenProvider, stdin io.Reader, stdout, stderr io.Writer) error {
	mode := tools.PermissionMode(opts.approval)
	if mode != tools.PermissionPrompt && mode != tools.PermissionAuto && mode != tools.PermissionAlwaysApprove && mode != tools.PermissionDeny {
		return fmt.Errorf("invalid --approval %q", opts.approval)
	}
	if cfg.DisableBypassPermissionsMode && mode == tools.PermissionAlwaysApprove {
		mode = tools.PermissionPrompt
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	authPath, _ := auth.DefaultPath()
	authConfig := auth.DefaultConfig()
	applyAuthPolicy(&authConfig, cfg)
	authMethodID := ""
	if tokenProvider != nil {
		authMethodID = "cached_token"
	} else if cfg.APIKey != "" {
		authMethodID = "xai.api_key"
	}
	cacheAuth, cacheOrigin := modelCacheIdentity(cfg, tokenProvider)
	modelCache, cacheLoaded := config.LoadModelCache(cacheAuth, cacheOrigin)
	if !cacheLoaded {
		if fetched, err := fetchACPModelCache(ctx, cfg, cacheAuth, cacheOrigin, authPath, authConfig.Scope()); err == nil {
			modelCache = fetched
		}
	}
	var modelCacheMu sync.RWMutex
	modelCacheSnapshot := func() config.ModelCache {
		modelCacheMu.RLock()
		defer modelCacheMu.RUnlock()
		return modelCache
	}
	refreshModelCache := func() {
		refreshed, _ := config.LoadModelCache(cacheAuth, cacheOrigin)
		modelCacheMu.Lock()
		modelCache = refreshed
		modelCacheMu.Unlock()
	}
	var extensionsMu sync.Mutex
	dynamicSkills := cloneSkillsConfig(cfg.Skills)
	dynamicPlugins := clonePluginsConfig(cfg.Plugins)
	pluginStates := make(map[*sessionPluginState]bool)
	var billingMetaMu sync.RWMutex
	var billingOnDemand *bool
	var billingTier *string
	setBillingMeta := func(current config.Config) {
		billingMetaMu.Lock()
		defer billingMetaMu.Unlock()
		billingOnDemand = current.OnDemandEnabled
		billingTier = current.SubscriptionTierDisplay
		if billingTier == nil {
			billingTier = current.SubscriptionTier
		}
	}
	getBillingMeta := func() (*bool, *string) {
		billingMetaMu.RLock()
		defer billingMetaMu.RUnlock()
		var onDemand *bool
		if billingOnDemand != nil {
			value := *billingOnDemand
			onDemand = &value
		}
		var tier *string
		if billingTier != nil {
			value := *billingTier
			tier = &value
		}
		return onDemand, tier
	}
	setBillingMeta(cfg)
	var server *acp.Server
	server = &acp.Server{SessionDir: opts.sessionDir, FolderTrustEnabled: cfg.FolderTrustEnabled, BillingMeta: getBillingMeta, Auth: acp.AuthConfig{
		Path: authPath, Scope: authConfig.Scope(), MethodID: authMethodID, Token: cfg.APIKey, TokenProvider: tokenProvider,
		ProxyBaseURL: cfg.ProxyBaseURL, HTTP: &http.Client{Timeout: cfg.HTTPTimeout},
	}, AuthChanged: func(authCtx context.Context, result auth.LogoutResult) error {
		if err := clearACPLogoutPolicy(authCtx, cfg, result); err != nil {
			return err
		}
		if shouldClearACPModelCache(cacheAuth, result) {
			if err := config.ClearModelCache(); err != nil {
				return err
			}
			modelCacheMu.Lock()
			modelCache = config.ModelCache{}
			modelCacheMu.Unlock()
		}
		return server.ReloadModels()
	}, Factory: func(
		sessionCtx context.Context,
		sessionConfig acp.SessionConfig,
		protocolApprover tools.Approver,
		textOutput io.Writer,
		statusOutput io.Writer,
	) (*agent.Runner, func(), error) {
		cfg := cfg
		if key, ok := auth.ReadAPIKeyEnvironment(); ok && !cfg.DisableAPIKeyAuth && !cfg.ForceLoginTeamConfigured && cfg.PreferredAuthMethod != "oidc" {
			cfg.APIKey = key
		}
		if tokenProvider != nil {
			refreshed, refreshErr := tokenProvider(sessionCtx, "")
			cfg.APIKey = ""
			if refreshErr == nil && refreshed != "" {
				cfg.APIKey = refreshed
				settingsCtx, cancel := context.WithTimeout(sessionCtx, 5*time.Second)
				remote := config.FetchRemoteSettings(settingsCtx, cfg.ProxyBaseURL, refreshed, &http.Client{Timeout: 3 * time.Second})
				cancel()
				if remote != nil {
					cfg.ApplyRemoteSettings(remote)
					setBillingMeta(cfg)
				}
			}
		}
		ws, err := workspace.Open(sessionConfig.CWD)
		if err != nil {
			return nil, nil, err
		}
		instructionFiles, err := ws.LoadInstructions(cfg.Compat)
		if err != nil {
			return nil, nil, err
		}
		projectTrusted := workspace.ResolveFolderTrust(ws.Root(), cfg.FolderTrustEnabled, false) == workspace.TrustTrusted
		if !projectTrusted {
			fmt.Fprintln(statusOutput, "[gork] project executable configuration is disabled for this untrusted workspace")
		}
		extensionsMu.Lock()
		sessionBase := cfg
		sessionBase.Skills = cloneSkillsConfig(dynamicSkills)
		sessionBase.Plugins = clonePluginsConfig(dynamicPlugins)
		extensionsMu.Unlock()
		sessionCfg, catalog, plugins, err := discoverWorkspace(ws.Root(), sessionBase, projectTrusted)
		if err != nil {
			return nil, nil, err
		}
		modelSource := sessionCfg
		sessionCfg.ApplyModelCache(modelCacheSnapshot())
		modelCatalog := sessionCfg
		var modelCatalogMu sync.RWMutex
		modelID, sessionCfg := resolveACPSessionModelEntry(sessionCfg, sessionConfig.Model, sessionConfig.ResumePath != "")
		reasoningEffort := sessionCfg.ReasoningEffort
		if sessionConfig.ReasoningEffort != "" && sessionCfg.ModelSupportsReasoningEffort {
			reasoningEffort = sessionConfig.ReasoningEffort
		}
		instructions := joinInstructions(cfg.SystemPrompt, workspace.FormatInstructions(instructionFiles), catalog.Summary())
		permissionPrompts := &permissionPromptApprover{base: protocolApprover}
		sessionMode := resolveACPSessionPermissionMode(
			mode, sessionConfig.YoloMode, sessionConfig.AutoMode,
			sessionCfg.DisableBypassPermissionsMode, sessionCfg.AutoModeEnabled(),
		)
		modeApprover, err := tools.NewModeApproverWithLocks(sessionMode, permissionPrompts, sessionCfg.DisableBypassPermissionsMode, !sessionCfg.AutoModeEnabled())
		if err != nil {
			return nil, nil, err
		}
		approver, err := tools.NewPolicyApprover(modeApprover, permissionPrompts, allowRules, askRules, denyRules)
		if err != nil {
			return nil, nil, err
		}
		registry := tools.NewRegistry(ws, approver)
		if err := registry.ConfigureFileToolset(sessionCfg.Toolset.FileToolset, sessionCfg.Toolset.Hashline.Scheme, sessionCfg.Toolset.Hashline.HashLen, sessionCfg.Toolset.Hashline.ChunkSize); err != nil {
			_ = registry.Close()
			return nil, nil, err
		}
		registry.ConfigureUserQuestions(sessionCfg.AskUserQuestion.TimeoutEnabled, time.Duration(sessionCfg.AskUserQuestion.TimeoutSeconds)*time.Second)
		registry.ConfigureGoalRoles(goalRoleConfig(sessionCfg, true))
		if search, enabled := cfg.WebSearchEndpoint(); enabled {
			if err := registry.Register(tools.NewWebSearchTool(search.BaseURL, search.APIKey, search.Model, &http.Client{Timeout: cfg.HTTPTimeout})); err != nil {
				_ = registry.Close()
				return nil, nil, err
			}
		}
		readPolicy, err := tools.NewPolicyApprover(
			tools.PromptApprover{Mode: tools.PermissionAuto}, permissionPrompts,
			allowRules, askRules, denyRules,
		)
		if err != nil {
			_ = registry.Close()
			return nil, nil, err
		}
		registry.SetReadPolicy(readPolicy)
		var logger *session.Logger
		if sessionConfig.ResumePath != "" {
			logger, _, err = session.Resume(sessionConfig.ResumePath)
		} else if sessionConfig.SessionID != "" {
			logger, err = session.NewLoggerWithID(opts.sessionDir, sessionConfig.SessionID)
		} else {
			logger, err = session.NewLogger(opts.sessionDir)
		}
		if err != nil {
			_ = registry.Close()
			return nil, nil, err
		}
		memoryStore, err := openMemoryStore(sessionCfg, ws.Root(), logger.ID())
		if err != nil {
			_ = logger.Close()
			_ = registry.Close()
			return nil, nil, err
		}
		if err := tools.RegisterMemoryTools(registry, memoryStore, sessionCfg.Memory); err != nil {
			_ = logger.Close()
			_ = registry.Close()
			return nil, nil, err
		}
		artifactDir, err := session.ArtifactDir(logger.Path())
		if err != nil {
			_ = logger.Close()
			_ = registry.Close()
			return nil, nil, err
		}
		registry.ConfigureWebFetch(tools.WebFetchConfig{
			ArtifactDir: artifactDir, ContextWindow: cfg.ContextWindow,
			ProxyEndpoint: cfg.WebFetch.ProxyEndpoint, AllowedDomains: cfg.WebFetch.AllowedDomains,
			RestrictDomains: cfg.WebFetch.DomainsConfigured,
		})
		if err := registry.ConfigureSchedulerState(artifactDir); err != nil {
			_ = logger.Close()
			_ = registry.Close()
			return nil, nil, err
		}
		if err := registry.ConfigurePlanMode(artifactDir); err != nil {
			_ = logger.Close()
			_ = registry.Close()
			return nil, nil, err
		}
		if err := registry.ConfigureGoalVerification(artifactDir); err != nil {
			_ = logger.Close()
			_ = registry.Close()
			return nil, nil, err
		}
		registry.SetGoalObserver(&sessionGoalObserver{server: server, sessionID: logger.ID(), logger: logger})
		registry.SetWebFetchEnabled(cfg.WebFetch.Enabled)
		if sessionConfig.ResumePath == "" {
			if err := logger.Append("session_metadata", sessionMetadata(ctx, ws.Root(), modelID, reasoningEffort)); err != nil {
				_ = logger.Close()
				_ = registry.Close()
				return nil, nil, err
			}
		}
		var mcpRuntime *sessionMCPRuntime
		var lspManager *lsp.Manager
		var subagentManager *subagent.Manager
		cleanup := func() {
			if subagentManager != nil {
				subagentManager.Close()
			}
			if lspManager != nil {
				_ = lspManager.Close()
			}
			if mcpRuntime != nil {
				mcpRuntime.Close()
			}
			_ = registry.Close()
			_ = logger.Close()
		}
		if err := registry.Register(catalog.Tool()); err != nil {
			cleanup()
			return nil, nil, err
		}
		mcpRuntime = newSessionMCPRuntime(sessionCtx, sessionCfg, ws.Root(), registry, approver, tokenProvider, statusOutput)
		if err = mcpRuntime.Update(sessionCtx, sessionConfig.MCPServers); err != nil {
			cleanup()
			return nil, nil, err
		}
		lspManager, err = startLSPServers(sessionCtx, sessionCfg, ws, registry, statusOutput)
		if err != nil {
			cleanup()
			return nil, nil, err
		}
		modelClient, err := newModelClient(sessionCfg, tokenProvider)
		if err != nil {
			cleanup()
			return nil, nil, err
		}
		permissionClassifier, err := newPermissionClassifierConfig(sessionCfg, tokenProvider)
		if err != nil {
			cleanup()
			return nil, nil, err
		}
		pluginState := &sessionPluginState{
			root: ws.Root(), trusted: projectTrusted, catalog: catalog,
			mcp: mcpRuntime, mcpSource: sessionBase,
			updateLSP: func(base config.Config) error {
				clients, err := startLSPClients(sessionCtx, base, ws, statusOutput)
				if err != nil {
					return err
				}
				if err := lspManager.Replace(clients); err != nil {
					for _, client := range clients {
						_ = client.Close()
					}
					return err
				}
				return nil
			},
			lspSource: sessionBase,
			inventory: append([]plugin.Plugin(nil), plugins...),
		}
		pluginState.hookCfg = hooks.Config{
			WorkspaceRoot: ws.Root(), Compat: sessionCfg.Compat, ProjectTrusted: projectTrusted, Plugins: plugins,
		}
		pluginState.hooks = hooks.Discover(pluginState.hookCfg)
		pluginState.hookRun = &hooks.Runtime{
			Catalog: pluginState.hooks, WorkspaceRoot: ws.Root(), SessionID: logger.ID(), Model: sessionCfg.Model,
		}
		permissionPrompts.SetNotify(func() {
			pluginState.hookRun.Notification(context.Background(), "permission_prompt", "Tool permission requested", "", "info")
		})
		processObserver := &sessionProcessObserver{server: server, sessionID: logger.ID(), logger: logger, hooks: pluginState.hookRun, autoWake: sessionCfg.AutoWakeEnabled, planApprover: permissionPrompts}
		registry.SetProcessObserver(processObserver)
		registry.SetSchedulerObserver(processObserver)
		registry.SetPlanModeObserver(processObserver)
		registry.SetUserQuestionObserver(processObserver)
		agentCatalog, agentErrors := agents.Discover(agents.Config{
			WorkspaceRoot: ws.Root(), ProjectTrusted: projectTrusted, Compat: sessionCfg.Compat, Plugins: plugins,
		})
		for _, agentErr := range agentErrors {
			fmt.Fprintln(statusOutput, "[gork] agent definition:", agentErr)
		}
		resolveSubagentModel := func(slug string) (subagent.ModelRuntime, bool) {
			modelCatalogMu.RLock()
			catalog := modelCatalog
			modelCatalogMu.RUnlock()
			resolved, ok := catalog.ResolveModel(slug)
			return subagent.ModelRuntime{Profile: slug, Model: resolved.Model, ContextWindow: resolved.ContextWindow, CompactThresholdPercent: resolved.AutoCompactThresholdPercent}, ok
		}
		subagentManager, err = subagent.New(subagent.Config{
			Context: sessionCtx, Catalog: agentCatalog, Tools: registry, WorkspaceRoot: ws.Root(), ParentModel: sessionCfg.Model,
			PermissionClassifier: permissionClassifier,
			ContextWindow:        sessionCfg.ContextWindow, CompactThresholdPercent: sessionCfg.AutoCompactThresholdPercent,
			TwoPassCompaction: sessionCfg.TwoPassCompaction,
			ResolveModel:      resolveSubagentModel, AvailableModels: sessionCfg.ModelSlugs(), Skills: catalog,
			SkillConfig: workspaceSkillsConfig(sessionCfg, plugins), Worktrees: server.WorktreeManager(),
			Observer:   &sessionSubagentObserver{server: server, sessionID: logger.ID(), logger: logger},
			SessionDir: filepath.Dir(logger.Path()), ParentSessionID: logger.ID(),
			AutoWake: func(result tools.SubagentResult) bool {
				return sessionCfg.AutoWakeEnabled && server.QueueSubagentWake(logger.ID(), result)
			},
			CancelWake:              func(id string) { server.CancelSubagentWake(logger.ID(), id) },
			DisablePermissionBypass: sessionCfg.DisableBypassPermissionsMode,
			ParentMCPServers:        mcpRuntime.Configs(),
			StartMCPServers: func(childCtx context.Context, root string, childTools *tools.Registry, servers []mcp.ServerConfig) (func(), error) {
				return startSubagentMCPServers(childCtx, sessionCfg, root, childTools, approver, tokenProvider, statusOutput, servers)
			},
			NewClient: func(model subagent.ModelRuntime) (agent.ResponseStreamer, error) {
				modelCatalogMu.RLock()
				child := modelCatalog
				modelCatalogMu.RUnlock()
				if model.Profile != "" {
					child, _ = child.ResolveModel(model.Profile)
				} else {
					child.Model = model.Model
				}
				return newModelClient(child, tokenProvider)
			}, Hooks: pluginState.hooks,
		})
		if err != nil {
			cleanup()
			return nil, nil, err
		}
		if err := registry.SetSubagentBackend(subagentManager); err != nil {
			cleanup()
			return nil, nil, err
		}
		watchCtx, stopSkills := context.WithCancel(sessionCtx)
		catalog.Watch(watchCtx, time.Second)
		pluginState.agents, pluginState.subagents = agentCatalog, subagentManager
		extensionsMu.Lock()
		pluginStates[pluginState] = true
		extensionsMu.Unlock()
		reloadMCPBase := func(updateCtx context.Context) error {
			pluginState.updateMu.Lock()
			defer pluginState.updateMu.Unlock()
			reloaded, err := config.Load(opts.configPath)
			if err != nil {
				return err
			}
			extensionsMu.Lock()
			source := pluginState.mcpSource
			inventory := append([]plugin.Plugin(nil), pluginState.inventory...)
			extensionsMu.Unlock()
			source.MCPServers = reloaded.MCPServers
			source.DisabledMCPServers = reloaded.DisabledMCPServers
			source.DisabledMCPTools = reloaded.DisabledMCPTools
			source.Compat = reloaded.Compat
			base := source
			base.MCPServers = config.DiscoverMCPServers(pluginState.root, base, enabledPlugins(inventory), pluginState.trusted)
			if err := pluginState.mcp.UpdateBase(updateCtx, base); err != nil {
				return err
			}
			extensionsMu.Lock()
			pluginState.mcpSource.MCPServers = reloaded.MCPServers
			pluginState.mcpSource.DisabledMCPServers = reloaded.DisabledMCPServers
			pluginState.mcpSource.DisabledMCPTools = reloaded.DisabledMCPTools
			pluginState.mcpSource.Compat = reloaded.Compat
			extensionsMu.Unlock()
			return nil
		}
		watchMCPConfig(sessionCtx, time.Second, func() ([]string, error) {
			extensionsMu.Lock()
			defer extensionsMu.Unlock()
			return config.MCPWatchPaths(pluginState.root, opts.configPath, pluginState.mcpSource, pluginState.inventory, pluginState.trusted), nil
		}, func() error { return reloadMCPBase(sessionCtx) }, statusOutput)
		var closeOnce sync.Once
		closeRuntime := func() {
			closeOnce.Do(func() {
				pluginState.hookRun.SessionEnded(context.Background(), "closed")
				stopSkills()
				extensionsMu.Lock()
				delete(pluginStates, pluginState)
				extensionsMu.Unlock()
				cleanup()
			})
		}
		updateSkills := func(_ context.Context, update func(*skills.Settings)) (skills.Settings, error) {
			if err := config.UpdateSkills(opts.configPath, func(stored *config.SkillsConfig) {
				settings := skillSettings(*stored)
				update(&settings)
				*stored = storedSkillSettings(settings)
			}); err != nil {
				return skills.Settings{}, err
			}
			reloaded, err := config.Load(opts.configPath)
			if err != nil {
				return skills.Settings{}, err
			}
			settings := cloneSkillsConfig(reloaded.Skills)
			extensionsMu.Lock()
			dynamicSkills = cloneSkillsConfig(settings)
			activeCatalogs := make([]*skills.Catalog, 0, len(pluginStates))
			for state := range pluginStates {
				activeCatalogs = append(activeCatalogs, state.catalog)
			}
			extensionsMu.Unlock()
			for _, active := range activeCatalogs {
				if err := active.Reconfigure(skillSettings(settings)); err != nil {
					return skills.Settings{}, err
				}
			}
			return skillSettings(settings), nil
		}
		pluginInventory := func() []plugin.Plugin {
			extensionsMu.Lock()
			defer extensionsMu.Unlock()
			return append([]plugin.Plugin(nil), pluginState.inventory...)
		}
		updatePlugins := func(updateCtx context.Context, update func(*plugin.Settings)) ([]plugin.Plugin, error) {
			if update != nil {
				if err := config.UpdatePlugins(opts.configPath, func(stored *config.PluginsConfig) {
					settings := pluginSettings(*stored)
					update(&settings)
					*stored = storedPluginSettings(settings)
				}); err != nil {
					return nil, err
				}
			}
			reloaded, err := config.Load(opts.configPath)
			if err != nil {
				return nil, err
			}
			settings := clonePluginsConfig(reloaded.Plugins)
			extensionsMu.Lock()
			dynamicPlugins = clonePluginsConfig(settings)
			states := make([]*sessionPluginState, 0, len(pluginStates))
			for state := range pluginStates {
				states = append(states, state)
			}
			extensionsMu.Unlock()
			for _, state := range states {
				state.updateMu.Lock()
				trusted := workspace.ResolveFolderTrust(state.root, cfg.FolderTrustEnabled, false) == workspace.TrustTrusted
				extensionsMu.Lock()
				previousInventory := append([]plugin.Plugin(nil), state.inventory...)
				previousTrusted := state.trusted
				mcpSource := state.mcpSource
				extensionsMu.Unlock()
				inventory, err := plugin.Inventory(state.root, plugin.Config{
					Paths: settings.Paths, Enabled: settings.Enabled, Disabled: settings.Disabled,
					ProjectTrusted: trusted,
				})
				if err != nil {
					state.updateMu.Unlock()
					return nil, err
				}
				if err := state.catalog.ReconfigurePlugins(enabledPlugins(inventory)); err != nil {
					state.updateMu.Unlock()
					return nil, err
				}
				mcpBase := mcpSource
				mcpBase.MCPServers = config.DiscoverMCPServers(state.root, mcpBase, enabledPlugins(inventory), trusted)
				if err := state.mcp.UpdateBase(updateCtx, mcpBase); err != nil {
					rollbackErr := state.catalog.ReconfigurePlugins(enabledPlugins(previousInventory))
					state.updateMu.Unlock()
					if rollbackErr != nil {
						return nil, errors.Join(err, fmt.Errorf("restore previous plugin catalog: %w", rollbackErr))
					}
					return nil, err
				}
				lspBase := state.lspSource
				lspBase.LSPServers = config.DiscoverLSPServers(state.root, lspBase, enabledPlugins(inventory), trusted)
				if err := state.updateLSP(lspBase); err != nil {
					var rollbackErr error
					if catalogErr := state.catalog.ReconfigurePlugins(enabledPlugins(previousInventory)); catalogErr != nil {
						rollbackErr = errors.Join(rollbackErr, catalogErr)
					}
					previousMCP := mcpSource
					previousMCP.MCPServers = config.DiscoverMCPServers(state.root, previousMCP, enabledPlugins(previousInventory), previousTrusted)
					if mcpErr := state.mcp.UpdateBase(updateCtx, previousMCP); mcpErr != nil {
						rollbackErr = errors.Join(rollbackErr, mcpErr)
					}
					state.updateMu.Unlock()
					return nil, errors.Join(err, rollbackErr)
				}
				extensionsMu.Lock()
				state.inventory = append([]plugin.Plugin(nil), inventory...)
				state.trusted = trusted
				extensionsMu.Unlock()
				state.hookCfg.Plugins = inventory
				state.hookCfg.ProjectTrusted = trusted
				state.hooks.Reconfigure(state.hookCfg)
				agentCatalog, _ := agents.Discover(agents.Config{
					WorkspaceRoot: state.root, ProjectTrusted: trusted, Compat: state.hookCfg.Compat, Plugins: inventory,
				})
				state.agents = agentCatalog
				state.subagents.SetCatalog(agentCatalog)
				state.updateMu.Unlock()
			}
			return pluginInventory(), nil
		}
		marketplaceAction := func(actionCtx context.Context, action marketplace.Action) (marketplace.Outcome, error) {
			outcome, err := marketplace.Execute(opts.configPath, ws.Root(), action)
			if err != nil || outcome.Status != "success" {
				return outcome, err
			}
			var update func(*plugin.Settings)
			switch action.Type {
			case "install", "uninstall", "update", "remove_source":
				update = func(settings *plugin.Settings) { applyMarketplacePlugins(settings, action.Type, outcome) }
			default:
				return outcome, nil
			}
			_, err = updatePlugins(actionCtx, update)
			return outcome, err
		}
		toggleMCPServer := func(updateCtx context.Context, name string, enabled bool) error {
			found := false
			for _, server := range pluginState.mcp.Catalog() {
				if server.Name == name {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("MCP server %q not found in config", name)
			}
			if err := config.SetMCPServerEnabled(opts.configPath, name, enabled); err != nil {
				return err
			}
			return reloadMCPBase(updateCtx)
		}
		toggleMCPTool := func(updateCtx context.Context, serverName, toolName string, enabled bool) error {
			found := false
			for _, server := range pluginState.mcp.Catalog() {
				if server.Name == serverName {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("MCP server %q not found in config", serverName)
			}
			if err := config.SetMCPToolEnabled(opts.configPath, serverName, toolName, enabled); err != nil {
				return err
			}
			return reloadMCPBase(updateCtx)
		}
		upsertMCPServer := func(updateCtx context.Context, server mcp.ServerConfig) error {
			enabled := true
			if err := config.UpsertMCPServer(opts.configPath, server.Name, config.MCPServerConfig{
				Type: server.Type, Command: server.Command, Args: append([]string(nil), server.Args...), Env: cloneStringsMap(server.Env),
				URL: server.URL, Headers: cloneStringsMap(server.Headers), Enabled: &enabled,
			}); err != nil {
				return err
			}
			return reloadMCPBase(updateCtx)
		}
		deleteMCPServer := func(updateCtx context.Context, name string) error {
			existed, err := config.DeleteMCPServer(opts.configPath, name)
			if err != nil {
				return err
			}
			if !existed {
				return fmt.Errorf("MCP server %q not found in config.toml", name)
			}
			return reloadMCPBase(updateCtx)
		}
		runner := &agent.Runner{
			Client: modelClient, Tools: registry, Skills: catalog, PluginInventory: pluginInventory, Logger: logger,
			HookCatalog: pluginState.hooks, HookPolicy: pluginState.hookRun,
			ListSubagents: subagentManager.List, GetSubagent: subagentManager.Output, KillSubagent: subagentManager.Kill,
			ListTasks: registry.BackgroundTasks, KillTask: registry.KillBackgroundTask,
			ReloadHooks: func() error {
				pluginState.updateMu.Lock()
				defer pluginState.updateMu.Unlock()
				extensionsMu.Lock()
				inventory := append([]plugin.Plugin(nil), pluginState.inventory...)
				extensionsMu.Unlock()
				pluginState.hookCfg.Plugins = inventory
				pluginState.hookCfg.ProjectTrusted = workspace.ResolveFolderTrust(pluginState.root, cfg.FolderTrustEnabled, false) == workspace.TrustTrusted
				pluginState.hooks.Reconfigure(pluginState.hookCfg)
				return nil
			},
			ModelID: modelID, Model: sessionCfg.Model, ModelOptions: acpModelOptions(modelCatalog), ReasoningEffort: reasoningEffort,
			Instructions: instructions, MaxSteps: cfg.MaxSteps,
			PermissionClassifier: permissionClassifier,
			TextOutput:           textOutput, StatusOutput: statusOutput,
			ContextWindow: sessionCfg.ContextWindow, CompactThresholdPercent: sessionCfg.AutoCompactThresholdPercent,
			TwoPassCompaction: sessionCfg.TwoPassCompaction,
			Memory:            memoryStore, MemoryConfig: sessionCfg.Memory,
			OpenMemory:    memoryStoreOpener(sessionCfg.Memory, ws.Root(), logger.ID()),
			ReloadMCPBase: reloadMCPBase, UpdateMCPServers: mcpRuntime.Update,
			MCPServers: mcpRuntime.Configs, MCPServerCatalog: mcpRuntime.Catalog,
			ToggleMCPServer: toggleMCPServer, ToggleMCPTool: toggleMCPTool,
			UpsertMCPServer: upsertMCPServer, DeleteMCPServer: deleteMCPServer,
			UpdateSkills:      updateSkills,
			UpdatePlugins:     updatePlugins,
			MarketplaceList:   func() ([]marketplace.ScanResult, error) { return marketplace.List(opts.configPath, ws.Root()) },
			MarketplaceAction: marketplaceAction,
			SessionPath:       logger.Path(),
		}
		runner.ResolveModel = func(id string) (agent.ModelRuntime, error) {
			modelCatalogMu.RLock()
			catalog := modelCatalog
			modelCatalogMu.RUnlock()
			resolvedID, resolved, ok := catalog.ResolveModelEntry(id)
			if !ok {
				return agent.ModelRuntime{}, fmt.Errorf("unknown model id %q", id)
			}
			client, err := newModelClient(resolved, tokenProvider)
			if err != nil {
				return agent.ModelRuntime{}, err
			}
			return agent.ModelRuntime{
				ID: resolvedID, Client: client, Model: resolved.Model,
				ContextWindow: resolved.ContextWindow, CompactThresholdPercent: resolved.AutoCompactThresholdPercent,
				ReasoningEffort: resolved.ReasoningEffort, SupportsReasoningEffort: resolved.ModelSupportsReasoningEffort,
			}, nil
		}
		runner.OnModelChanged = func(runtime agent.ModelRuntime) {
			subagentManager.SetParentModel(runtime.Model, runtime.ContextWindow, runtime.CompactThresholdPercent)
			modelCatalogMu.RLock()
			catalog := modelCatalog
			modelCatalogMu.RUnlock()
			if _, resolved, ok := catalog.ResolveModelEntry(runtime.ID); ok {
				registry.ConfigureGoalRoles(goalRoleConfig(resolved, true))
			}
		}
		reloadModels := func(cacheOnly bool) (agent.ModelCatalogUpdate, error) {
			var reloaded config.Config
			if cacheOnly {
				refreshModelCache()
			} else {
				var err error
				reloaded, err = config.Load(opts.configPath)
				if err != nil {
					return agent.ModelCatalogUpdate{}, err
				}
			}
			cache := modelCacheSnapshot()
			modelCatalogMu.Lock()
			previous := modelCatalog
			if !cacheOnly {
				modelSource.ReloadModelCatalog(reloaded)
			}
			next := modelSource
			next.ApplyModelCache(cache)
			if opts.model != "" {
				next.Model, next.DefaultModelID = opts.model, ""
			}
			oldPreferred := preferredACPModelID(previous)
			newPreferred := preferredACPModelID(next)
			changed := modelCatalogChanged(previous, next)
			if changed {
				modelCatalog = next
			}
			modelCatalogMu.Unlock()
			if changed {
				subagentManager.SetAvailableModels(next.ModelSlugs())
			}
			return agent.ModelCatalogUpdate{
				Options: acpModelOptions(next), PreferredID: newPreferred,
				PreferredChanged: oldPreferred != newPreferred && next.HasExplicitModelPreference(),
				Changed:          changed,
			}, nil
		}
		runner.ReloadModels = func() (agent.ModelCatalogUpdate, error) { return reloadModels(false) }
		runner.ReloadModelCache = func() (agent.ModelCatalogUpdate, error) { return reloadModels(true) }
		return runner, func() {
			waitRunnerMemory(runner)
			closeRuntime()
		}, nil
	}}
	watchModelConfig(ctx, time.Second, config.ModelWatchPaths(opts.configPath), server.ReloadModels, stderr)
	if err := server.Serve(ctx, stdin, stdout); err != nil {
		fmt.Fprintln(stderr, "[gork] ACP server failed:", err)
		return err
	}
	return nil
}

func clearACPLogoutPolicy(ctx context.Context, cfg config.Config, result auth.LogoutResult) error {
	if !result.ClearedCurrent || cfg.DeploymentKey != "" || (result.WasLoggedIn && result.Credential.TeamID == "") {
		return nil
	}
	home, err := config.PolicyHome()
	if err != nil {
		return err
	}
	return config.ClearManagedPolicy(ctx, home)
}

func goalRoleConfig(cfg config.Config, goalEnabled bool) tools.GoalRoleConfig {
	convert := func(model config.GoalRoleModel) tools.GoalRoleModel {
		return tools.GoalRoleModel{Model: model.Model, AgentType: model.AgentType}
	}
	result := tools.GoalRoleConfig{
		CurrentModel: cfg.Model, ClassifierMaxRuns: cfg.Goal.ClassifierMaxRuns,
		PlannerEnabled: cfg.GoalPlannerEnabled(goalEnabled), StrategistEvery: cfg.GoalStrategistEvery(),
		SummaryEnabled: cfg.GoalSummaryEnabled(goalEnabled), UseCurrentModelOnly: cfg.Goal.UseCurrentModelOnly,
	}
	if cfg.Goal.PlannerModel != nil {
		result.Planner = convert(*cfg.Goal.PlannerModel)
	}
	if cfg.Goal.StrategistModel != nil {
		result.Strategist = convert(*cfg.Goal.StrategistModel)
	}
	for _, model := range cfg.Goal.SkepticModels {
		result.Skeptics = append(result.Skeptics, convert(model))
	}
	return result
}

func appendGoalPlanReminder(prompt, path string) string {
	if path == "" {
		return prompt
	}
	return prompt + "\n\nThe goal plan is the verification contract. Read it before acting and keep the implementation aligned with it:\n" + path
}

func applyMarketplacePlugins(settings *plugin.Settings, action string, outcome marketplace.Outcome) {
	removed := outcome.RemovedPlugins
	if action == "uninstall" || action == "remove_source" {
		removed = outcome.Plugins
	}
	for _, name := range removed {
		settings.Enabled = slices.DeleteFunc(settings.Enabled, func(value string) bool { return value == name })
		settings.Disabled = slices.DeleteFunc(settings.Disabled, func(value string) bool { return value == name })
	}
	if action == "uninstall" || action == "remove_source" {
		return
	}
	for _, name := range outcome.Plugins {
		if !slices.Contains(settings.Enabled, name) {
			settings.Enabled = append(settings.Enabled, name)
		}
		settings.Disabled = slices.DeleteFunc(settings.Disabled, func(value string) bool { return value == name })
	}
}

func cloneSkillsConfig(source config.SkillsConfig) config.SkillsConfig {
	return config.SkillsConfig{
		Paths: append([]string(nil), source.Paths...), Ignore: append([]string(nil), source.Ignore...),
		Disabled: append([]string(nil), source.Disabled...),
	}
}

func workspaceSkillsConfig(cfg config.Config, inventory []plugin.Plugin) skills.Config {
	return skills.Config{
		Compat: cfg.Compat, Paths: append([]string(nil), cfg.Skills.Paths...),
		Ignore: append([]string(nil), cfg.Skills.Ignore...), Disabled: append([]string(nil), cfg.Skills.Disabled...),
		Plugins: enabledPlugins(inventory),
	}
}

func clonePluginsConfig(source config.PluginsConfig) config.PluginsConfig {
	return config.PluginsConfig{
		Paths: append([]string(nil), source.Paths...), Enabled: append([]string(nil), source.Enabled...),
		Disabled: append([]string(nil), source.Disabled...),
	}
}

func pluginSettings(source config.PluginsConfig) plugin.Settings {
	return plugin.Settings{
		Paths: append([]string(nil), source.Paths...), Enabled: append([]string(nil), source.Enabled...),
		Disabled: append([]string(nil), source.Disabled...),
	}
}

func storedPluginSettings(source plugin.Settings) config.PluginsConfig {
	return config.PluginsConfig{
		Paths: append([]string(nil), source.Paths...), Enabled: append([]string(nil), source.Enabled...),
		Disabled: append([]string(nil), source.Disabled...),
	}
}

func enabledPlugins(inventory []plugin.Plugin) []plugin.Plugin {
	enabled := make([]plugin.Plugin, 0, len(inventory))
	for _, item := range inventory {
		if item.Enabled {
			enabled = append(enabled, item)
		}
	}
	return enabled
}

func skillSettings(source config.SkillsConfig) skills.Settings {
	return skills.Settings{
		Paths: append([]string(nil), source.Paths...), Ignore: append([]string(nil), source.Ignore...),
		Disabled: append([]string(nil), source.Disabled...),
	}
}

func storedSkillSettings(source skills.Settings) config.SkillsConfig {
	return config.SkillsConfig{
		Paths: append([]string(nil), source.Paths...), Ignore: append([]string(nil), source.Ignore...),
		Disabled: append([]string(nil), source.Disabled...),
	}
}

func discoverWorkspace(root string, cfg config.Config, projectTrusted bool) (config.Config, *skills.Catalog, []plugin.Plugin, error) {
	_ = plugin.RefreshLocal()
	inventory, err := plugin.Inventory(root, plugin.Config{
		Paths: cfg.Plugins.Paths, Enabled: cfg.Plugins.Enabled, Disabled: cfg.Plugins.Disabled,
		ProjectTrusted: projectTrusted,
	})
	if err != nil {
		return config.Config{}, nil, nil, err
	}
	plugins := make([]plugin.Plugin, 0, len(inventory))
	for _, item := range inventory {
		if item.Enabled {
			plugins = append(plugins, item)
		}
	}
	cfg.MCPServers = config.DiscoverMCPServers(root, cfg, plugins, projectTrusted)
	cfg.LSPServers = config.DiscoverLSPServers(root, cfg, plugins, projectTrusted)
	catalog, err := skills.Discover(root, workspaceSkillsConfig(cfg, plugins))
	return cfg, catalog, inventory, err
}

func resolveProjectTrust(ctx context.Context, root string, cfg config.Config, explicit bool, input *bufio.Reader, output io.Writer, interactive bool) (bool, error) {
	if explicit {
		if err := workspace.GrantFolderTrust(ctx, root); err != nil {
			return false, err
		}
		return true, nil
	}
	switch workspace.ResolveFolderTrust(root, cfg.FolderTrustEnabled, interactive) {
	case workspace.TrustTrusted:
		return true, nil
	case workspace.TrustPrompt:
		fmt.Fprintf(output, "Trust executable project configuration in %s? [y/N] ", workspace.WorkspaceTrustKey(root))
		answer, err := input.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return false, err
		}
		answer = strings.TrimSpace(answer)
		if strings.EqualFold(answer, "y") || strings.EqualFold(answer, "yes") {
			if err := workspace.GrantFolderTrust(ctx, root); err != nil {
				return false, err
			}
			return true, nil
		}
	}
	fmt.Fprintln(output, "[gork] project executable configuration is disabled; rerun with --trust to enable it")
	return false, nil
}

func terminalIO(value any) bool {
	file, ok := value.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func sessionMetadata(ctx context.Context, cwd, model, reasoningEffort string) map[string]any {
	metadata := map[string]any{"cwd": cwd, "modelId": model}
	if reasoningEffort != "" {
		metadata["reasoningEffort"] = reasoningEffort
	}
	if head, err := worktrees.Head(ctx, cwd); err == nil && head != "" {
		metadata["headCommit"] = head
	}
	if info, err := worktrees.HeadInfo(ctx, cwd); err == nil && info.Branch != "" {
		metadata["headBranch"] = info.Branch
	}
	return metadata
}

func goalLoop(
	ctx context.Context,
	runner *agent.Runner,
	registry *tools.Registry,
	stdout io.Writer,
	stderr io.Writer,
	objective string,
	previousResponseID string,
	maxRuns int,
	verifierCount int,
	classifierMaxRuns uint32,
	reverifyAfter uint32,
) error {
	prompt := objective
	for run := 1; run <= maxRuns; run++ {
		fmt.Fprintf(stderr, "[gork] goal run %d/%d\n", run, maxRuns)
		result, err := runner.RunTurn(ctx, prompt, previousResponseID)
		var workerErr error
		if err == nil {
			workerErr = registry.RecordGoalWorkerRound()
		}
		registry.AddGoalTokens(result.TokensUsed)
		if err != nil {
			if snapshot, limited := registry.EnforceGoalBudget(); limited {
				fmt.Fprintf(stderr, "[gork] Goal token budget reached (%d of %d tokens) - goal stopped.\n", snapshot.TokensUsed, snapshot.TokenBudget)
				return nil
			}
			if errors.Is(err, context.Canceled) {
				return errors.Join(err, registry.PauseGoalUser())
			}
			return errors.Join(err, registry.PauseGoalInfrastructure(err))
		}
		if workerErr != nil {
			return workerErr
		}
		previousResponseID = result.ResponseID
		if result.Text != "" && !strings.HasSuffix(result.Text, "\n") {
			fmt.Fprintln(stdout)
		}
		snapshot := registry.GoalSnapshot()
		switch snapshot.Status {
		case "verifying":
			fmt.Fprintln(stderr, "[gork] verifying goal completion with independent skeptics")
			if err := registry.StartGoalVerification(classifierMaxRuns); err != nil {
				return err
			}
			snapshot = registry.GoalSnapshot()
			verification := registry.VerifyGoal(ctx, snapshot, verifierCount)
			if err := registry.ResolveGoalVerification(verification, classifierMaxRuns); err != nil {
				return err
			}
			if verification.DetailsPath != "" {
				fmt.Fprintln(stderr, "[gork] goal verification details:", verification.DetailsPath)
			}
			if current := registry.GoalSnapshot(); current.Status == "back_off_paused" || current.Status == "no_progress_paused" {
				return errors.New("goal verification paused: " + current.Message)
			}
			if verification.Achieved {
				fmt.Fprintln(stderr, "[gork] goal completed:", verification.Summary)
				if summary := registry.RunGoalSummarizer(ctx, verification); summary != "" {
					fmt.Fprintln(stdout)
					fmt.Fprintln(stdout, summary)
				}
				return nil
			}
			fmt.Fprintln(stderr, "[gork] goal completion refuted:", verification.Summary)
			if current, limited := registry.EnforceGoalBudget(); limited {
				fmt.Fprintf(stderr, "[gork] Goal token budget reached (%d of %d tokens) - goal stopped.\n", current.TokensUsed, current.TokenBudget)
				return nil
			}
			prompt = "Continue working toward the full objective. Do not claim completion again until every verifier gap is resolved."
			if strategy := registry.RunGoalStrategist(ctx); strategy != "" {
				fmt.Fprintln(stderr, "[gork] goal strategist produced a structural recommendation")
				prompt += "\n\n" + strategy
			}
			if current, limited := registry.EnforceGoalBudget(); limited {
				fmt.Fprintf(stderr, "[gork] Goal token budget reached (%d of %d tokens) - goal stopped.\n", current.TokensUsed, current.TokenBudget)
				return nil
			}
			prompt = appendGoalPlanReminder(prompt, registry.GoalSnapshot().PlanPath)
			scratch, scratchReady := registry.GoalScratch()
			prompt = appendGoalScratchReminder(prompt, scratch, scratchReady)
			prompt = appendGoalVerificationGaps(prompt, registry.GoalVerificationGaps())
			prompt = appendGoalNextStep(prompt, registry.GoalNextStep())
			reminder, err := registry.GoalReverifyReminder(reverifyAfter)
			if err != nil {
				return err
			}
			prompt = appendGoalReverifyReminder(prompt, reminder)
			continue
		case "completed":
			fmt.Fprintln(stderr, "[gork] goal completed:", snapshot.Message)
			return nil
		case "blocked":
			return fmt.Errorf("goal blocked: %s", snapshot.Message)
		case "budget_limited":
			fmt.Fprintf(stderr, "[gork] Goal token budget reached (%d of %d tokens) - goal stopped.\n", snapshot.TokensUsed, snapshot.TokenBudget)
			return nil
		}
		if current, limited := registry.EnforceGoalBudget(); limited {
			fmt.Fprintf(stderr, "[gork] Goal token budget reached (%d of %d tokens) - goal stopped.\n", current.TokensUsed, current.TokenBudget)
			return nil
		}
		continuation := "Continue working toward the active goal. Verify the remaining work, then call update_goal with progress, completed=true only when fully achieved, or blocked_reason only if genuinely stuck."
		if pattern := registry.DetectGoalPrematureStop(result.Text); pattern != "" {
			continuation = "Your previous response ended prematurely while actionable work remains. Continue now: execute the pending tasks, inspect results, and keep working until the goal is independently verifiable. Do not hand work back to the user or stop merely because an agent, command, review, or follow-up is pending."
		}
		prompt = appendGoalPlanReminder(continuation, snapshot.PlanPath)
		scratch, scratchReady := registry.GoalScratch()
		prompt = appendGoalScratchReminder(prompt, scratch, scratchReady)
		prompt = appendGoalVerificationGaps(prompt, registry.GoalVerificationGaps())
		prompt = appendGoalNextStep(prompt, registry.GoalNextStep())
		reminder, err := registry.GoalReverifyReminder(reverifyAfter)
		if err != nil {
			return err
		}
		prompt = appendGoalReverifyReminder(prompt, reminder)
	}
	return fmt.Errorf("goal remains active after %d runs", maxRuns)
}

var trailingGoalBudget = regexp.MustCompile(`(?s)^(.*\S)\s+--budget\s+([0-9]+)\s*$`)

func parseGoalBudget(objective string) (string, int64) {
	trimmed := strings.TrimSpace(objective)
	match := trailingGoalBudget.FindStringSubmatch(trimmed)
	if match == nil {
		return trimmed, 0
	}
	budget, err := strconv.ParseInt(match[2], 10, 64)
	if err != nil || budget <= 0 {
		return trimmed, 0
	}
	return strings.TrimSpace(match[1]), budget
}

func appendGoalNextStep(prompt, step string) string {
	if step == "" {
		step = "Check your `todo_write` list for next steps."
	}
	return prompt + "\n\nGoal NOT complete - continue working. Next step:\n" + step
}

func appendGoalVerificationGaps(prompt, gaps string) string {
	if gaps == "" {
		return prompt
	}
	return prompt + "\n\nVerification REJECTED your last `update_goal(completed: true)` claim. Fix every gap the skeptic panel flagged below - these take priority - before claiming completion again:\n" + gaps
}

func appendGoalScratchReminder(prompt, scratch string, ready bool) string {
	if scratch == "" {
		return prompt
	}
	status := "is unavailable; keep transient evidence inside the workspace"
	if ready {
		status = "has been created for you"
	}
	return prompt + "\n\nSave captured test output and throwaway artifacts in your private scratch directory " + scratch + " (" + status + "), never shared /tmp. The plan's `{SCRATCH}` placeholder resolves to this directory."
}

func appendGoalReverifyReminder(prompt, reminder string) string {
	if reminder == "" {
		return prompt
	}
	return prompt + "\n\n" + reminder
}

func startLSPServers(
	ctx context.Context,
	cfg config.Config,
	ws *workspace.Workspace,
	registry *tools.Registry,
	stderr io.Writer,
) (*lsp.Manager, error) {
	manager := lsp.NewManager(ws)
	clients, err := startLSPClients(ctx, cfg, ws, stderr)
	if err != nil {
		_ = manager.Close()
		return nil, err
	}
	if err := manager.Replace(clients); err != nil {
		for _, client := range clients {
			_ = client.Close()
		}
		_ = manager.Close()
		return nil, err
	}
	if err := registry.Register(manager.Tool()); err != nil {
		_ = manager.Close()
		return nil, fmt.Errorf("register LSP tool: %w", err)
	}
	return manager, nil
}

func startLSPClients(ctx context.Context, cfg config.Config, ws *workspace.Workspace, stderr io.Writer) ([]*lsp.Client, error) {
	names := make([]string, 0, len(cfg.LSPServers))
	for name, server := range cfg.LSPServers {
		if server.IsEnabled() {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	clients := make([]*lsp.Client, 0, len(names))
	closeClients := func() {
		for _, client := range clients {
			_ = client.Close()
		}
	}
	for _, name := range names {
		server := cfg.LSPServers[name]
		root := ws.Root()
		if server.WorkspaceFolder != "" {
			var err error
			root, err = ws.Resolve(server.WorkspaceFolder)
			if err != nil {
				closeClients()
				return nil, fmt.Errorf("resolve LSP workspace for %q: %w", name, err)
			}
		}
		fmt.Fprintf(stderr, "[gork] starting LSP server: %s\n", name)
		maxRestarts := 3
		if server.MaxRestarts != nil {
			maxRestarts = *server.MaxRestarts
		}
		client, err := lsp.Start(ctx, lsp.ProcessConfig{
			Name: name, Command: server.Command, Transport: server.Transport, Args: server.Args,
			Env: server.Env, Extensions: server.Extensions,
			InitializationOptions: server.InitializationOptions, Settings: server.Settings,
			Root: root, Stderr: stderr,
			StartupTimeout:  time.Duration(server.StartupTimeoutMS) * time.Millisecond,
			ShutdownTimeout: time.Duration(server.ShutdownTimeoutMS) * time.Millisecond,
			RestartOnCrash:  server.RestartOnCrash,
			MaxRestarts:     maxRestarts,
		})
		if err != nil {
			closeClients()
			return nil, err
		}
		clients = append(clients, client)
		fmt.Fprintf(stderr, "[gork] LSP %s ready\n", name)
	}
	return clients, nil
}

func joinInstructions(parts ...string) string {
	var kept []string
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			kept = append(kept, trimmed)
		}
	}
	return strings.Join(kept, "\n\n")
}

func interactiveLoop(
	ctx context.Context,
	runner *agent.Runner,
	scheduled *scheduledWakeQueue,
	input *terminalInput,
	stdout io.Writer,
	stderr io.Writer,
	initialPrompt string,
	previousResponseID string,
) error {
	fmt.Fprintln(stderr, "[gork] interactive mode; /exit to quit, /help for commands")
	prompt := strings.TrimSpace(initialPrompt)
	rememberMode := false
	inputClosed := false
	promptShown := false
	var promptRead <-chan terminalReadResult
	for {
		scheduledID := ""
		if prompt == "" {
			if inputClosed {
				return nil
			}
			if promptRead == nil {
				promptRead = input.request(ctx, false)
			}
			var read terminalReadResult
			select {
			case read = <-promptRead:
				promptRead = nil
			default:
				if event, ok := scheduled.Take(); ok {
					prompt, scheduledID = event.Prompt, event.TaskID
					promptShown = false
					fmt.Fprintf(stderr, "\n[gork] scheduled task %s fired\n", event.TaskID)
					break
				}
				if !promptShown {
					fmt.Fprint(stderr, "\ngork> ")
					promptShown = true
				}
				select {
				case read = <-promptRead:
					promptRead = nil
				case <-scheduled.Notify():
					continue
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			if scheduledID == "" {
				if read.err != nil && !errors.Is(read.err, io.EOF) {
					return fmt.Errorf("read interactive prompt: %w", read.err)
				}
				prompt = strings.TrimSpace(read.line)
				inputClosed = errors.Is(read.err, io.EOF)
				promptShown = false
				if inputClosed && prompt == "" {
					return nil
				}
			}
		}
		if scheduledID == "" {
			if rememberMode {
				if prompt == "" {
					fmt.Fprintln(stderr, "[gork] Please provide a memory note.")
					continue
				}
				rememberMode = false
				if err := reviewMemoryNoteTerminal(ctx, runner, input, stderr, prompt); err != nil {
					fmt.Fprintln(stderr, "[gork] memory note failed:", err)
				}
				prompt = ""
				continue
			}
			if note, ok := tools.ParseRememberCommand(prompt); ok {
				if note == "" {
					rememberMode = true
					fmt.Fprintln(stderr, "[gork] Enter a memory note.")
				} else if err := reviewMemoryNoteTerminal(ctx, runner, input, stderr, note); err != nil {
					fmt.Fprintln(stderr, "[gork] memory note failed:", err)
				}
				prompt = ""
				continue
			}
			if action, ok := tools.ParseMemoryCommand(prompt); ok {
				switch action {
				case "enable", "disable":
					message, err := runner.SetMemoryEnabled(ctx, action == "enable")
					if err != nil {
						fmt.Fprintln(stderr, "[gork] memory toggle failed:", err)
					} else {
						fmt.Fprintln(stderr, "[gork]", message)
					}
				case "browse":
					files, err := runner.ListMemory()
					if err != nil {
						fmt.Fprintln(stderr, "[gork] memory list failed:", err)
					} else if len(files) == 0 {
						fmt.Fprintln(stderr, "[gork] memory: no files")
					} else {
						for _, file := range files {
							fmt.Fprintf(stderr, "[gork] memory %s %d %s\n", file.Source, file.SizeBytes, file.Path)
						}
					}
				}
				prompt = ""
				continue
			}
			switch prompt {
			case "":
				continue
			case "/exit", "/quit":
				return nil
			case "/help":
				fmt.Fprintln(stderr, "Commands: ! <command>, /compact, /flush, /dream, /remember [text], /memory [on|off], /loop, /help, /exit. Every other line is sent as a prompt.")
				prompt = ""
				continue
			case "/compact":
				if _, err := runner.Compact(ctx, previousResponseID); err != nil {
					fmt.Fprintln(stderr, "[gork] compact failed:", err)
				} else {
					previousResponseID = ""
				}
				prompt = ""
				continue
			case "/flush":
				result, err := runner.FlushMemory(ctx, previousResponseID)
				if err != nil {
					fmt.Fprintln(stderr, "[gork] memory flush failed:", err)
				} else {
					fmt.Fprintln(stderr, "[gork] memory flush:", result.Outcome)
				}
				prompt = ""
				continue
			case "/dream":
				result, err := runner.DreamMemory(ctx, true)
				if err != nil {
					fmt.Fprintln(stderr, "[gork] memory dream failed:", err)
				} else {
					fmt.Fprintln(stderr, "[gork] memory dream:", result.Outcome)
				}
				prompt = ""
				continue
			}
			if strings.HasPrefix(prompt, "!") {
				command := strings.TrimSpace(strings.TrimPrefix(prompt, "!"))
				if command == "" {
					fmt.Fprintln(stderr, "[gork] shell command is empty")
				} else {
					output, err := runner.RunShell(ctx, command)
					if output != "" {
						fmt.Fprint(stdout, output)
						if !strings.HasSuffix(output, "\n") {
							fmt.Fprintln(stdout)
						}
					}
					if err != nil {
						fmt.Fprintln(stderr, "[gork] shell failed:", err)
					}
				}
				prompt = ""
				continue
			}
			prompt, _ = tools.ExpandLoopCommand(prompt)
		}
		var result agent.Result
		var err error
		if scheduledID != "" {
			result, err = runner.RunSyntheticTurn(ctx, prompt, previousResponseID)
			scheduled.Done(scheduledID)
		} else {
			result, err = runner.RunTurn(ctx, prompt, previousResponseID)
		}
		if err != nil {
			fmt.Fprintln(stderr, "[gork] turn failed:", err)
			if ctx.Err() != nil {
				return ctx.Err()
			}
		} else {
			previousResponseID = result.ResponseID
			if result.Text != "" && !strings.HasSuffix(result.Text, "\n") {
				fmt.Fprintln(stdout)
			}
		}
		prompt = ""
		if inputClosed {
			return nil
		}
	}
}

func reviewMemoryNoteTerminal(ctx context.Context, runner *agent.Runner, input *terminalInput, output io.Writer, raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return errors.New("memory note is empty")
	}
	enhanced := runner.EnhanceMemoryNote(ctx, raw)
	fmt.Fprintf(output, "\nMemory note (raw):\n%s\n", raw)
	if enhanced != "" && enhanced != raw {
		fmt.Fprintf(output, "\nMemory note (enhanced):\n%s\n", enhanced)
	}
	fmt.Fprint(output, "\nSave [y/raw], enhanced [e], or cancel [N]? ")
	read := input.request(ctx, true)
	var result terminalReadResult
	select {
	case result = <-read:
	case <-ctx.Done():
		return ctx.Err()
	}
	if result.err != nil && !errors.Is(result.err, io.EOF) {
		return result.err
	}
	if errors.Is(result.err, io.EOF) && strings.TrimSpace(result.line) == "" {
		fmt.Fprintln(output, "[gork] memory note cancelled")
		return nil
	}
	choice := strings.ToLower(strings.TrimSpace(result.line))
	content := raw
	switch choice {
	case "", "y", "yes", "r", "raw":
	case "e", "enhanced":
		if enhanced == "" {
			fmt.Fprintln(output, "[gork] enhanced note unavailable; cancelled")
			return nil
		}
		content = enhanced
	default:
		fmt.Fprintln(output, "[gork] memory note cancelled")
		return nil
	}
	path, err := runner.SaveMemoryNote(content)
	if err != nil {
		return err
	}
	fmt.Fprintln(output, "[gork] Memory saved to", path)
	return nil
}

func runHeadless(ctx context.Context, runner *agent.Runner, scheduled *scheduledWakeQueue, stdout, stderr io.Writer, prompt, previousResponseID string) error {
	prompt, _ = tools.ExpandLoopCommand(prompt)
	result, err := runner.RunTurn(ctx, prompt, previousResponseID)
	if err != nil {
		return err
	}
	previousResponseID = result.ResponseID
	if result.Text != "" && !strings.HasSuffix(result.Text, "\n") {
		fmt.Fprintln(stdout)
	}
	for {
		event, ok, err := scheduled.Next(ctx)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		fmt.Fprintf(stderr, "[gork] scheduled task %s fired\n", event.TaskID)
		result, err = runner.RunSyntheticTurn(ctx, event.Prompt, previousResponseID)
		scheduled.Done(event.TaskID)
		if err != nil {
			return err
		}
		previousResponseID = result.ResponseID
		if result.Text != "" && !strings.HasSuffix(result.Text, "\n") {
			fmt.Fprintln(stdout)
		}
	}
}

func startMCPServers(
	ctx context.Context,
	cfg config.Config,
	workspaceRoot string,
	registry *tools.Registry,
	approver tools.Approver,
	tokenProvider api.TokenProvider,
	stderr io.Writer,
) ([]*mcp.Client, error) {
	names := make([]string, 0, len(cfg.MCPServers))
	for name, server := range cfg.MCPServers {
		if server.IsEnabled() {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	clients := make([]*mcp.Client, 0, len(names))
	closeClients := func() {
		for _, client := range clients {
			_ = client.Close()
		}
	}
	for _, name := range names {
		server := cfg.MCPServers[name]
		sampling := newMCPSamplingHandler(cfg, approver, tokenProvider, name)
		fmt.Fprintf(stderr, "[gork] starting MCP server: %s\n", name)
		var client *mcp.Client
		var initialized mcp.InitializeResult
		var err error
		if server.URL != "" {
			httpConfig := mcp.HTTPConfig{Name: name, URL: server.URL, Headers: mcpHTTPHeaders(server)}
			transport := strings.ToLower(strings.TrimSpace(server.Type))
			if transport != "" && transport != "sse" && transport != "http" && transport != "streamable-http" {
				err = fmt.Errorf("MCP server %q has unsupported transport type %q", name, server.Type)
			} else if transport == "sse" || strings.HasSuffix(strings.TrimRight(server.URL, "/"), "/sse") {
				httpConfig.Sampling = sampling
				client, initialized, err = mcp.StartSSE(ctx, httpConfig)
			} else {
				httpConfig.Client = &http.Client{Timeout: cfg.HTTPTimeout}
				client, initialized, err = mcp.StartHTTP(ctx, httpConfig)
			}
		} else {
			client, initialized, err = mcp.Start(ctx, mcp.ProcessConfig{
				Name: name, Command: server.Command, Args: server.Args,
				Env: server.Env, Dir: workspaceRoot, Stderr: stderr, Sampling: sampling,
			})
		}
		if err != nil {
			closeClients()
			return nil, err
		}
		clients = append(clients, client)
		var remoteTools []mcp.ToolInfo
		if initialized.Capabilities.Tools != nil {
			listCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
			remoteTools, err = client.ListTools(listCtx)
			cancel()
			if err != nil {
				closeClients()
				return nil, fmt.Errorf("list tools from MCP server %q: %w", name, err)
			}
		}
		disabledTools := make(map[string]bool, len(cfg.DisabledMCPTools[name]))
		for _, toolName := range cfg.DisabledMCPTools[name] {
			disabledTools[toolName] = true
		}
		remoteTools = slices.DeleteFunc(remoteTools, func(tool mcp.ToolInfo) bool { return disabledTools[tool.Name] })
		toolAdapters := mcp.NewToolAdapters(client, name, remoteTools, approver)
		remoteNames := make([]string, 0, len(toolAdapters))
		for _, adapter := range toolAdapters {
			if err := registry.Register(adapter); err != nil {
				closeClients()
				return nil, fmt.Errorf("register MCP tool from %q: %w", name, err)
			}
			remoteNames = append(remoteNames, adapter.Definition().Name)
		}
		if initialized.Capabilities.Tools != nil && initialized.Capabilities.Tools.ListChanged {
			var reloadMu sync.Mutex
			client.SetNotificationHandler(func(method string) {
				if method != "notifications/tools/list_changed" {
					return
				}
				reloadMu.Lock()
				defer reloadMu.Unlock()
				reloadCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
				updated, listErr := client.ListTools(reloadCtx)
				cancel()
				if listErr != nil {
					fmt.Fprintf(stderr, "[gork] MCP %s tool reload failed: %v\n", name, listErr)
					return
				}
				updated = slices.DeleteFunc(updated, func(tool mcp.ToolInfo) bool { return disabledTools[tool.Name] })
				updatedAdapters := mcp.NewToolAdapters(client, name, updated, approver)
				replacements := make([]tools.Tool, 0, len(updatedAdapters))
				for _, adapter := range updatedAdapters {
					replacements = append(replacements, adapter)
				}
				newNames, replaceErr := registry.Replace(remoteNames, replacements)
				if replaceErr != nil {
					fmt.Fprintf(stderr, "[gork] MCP %s tool reload failed: %v\n", name, replaceErr)
					return
				}
				remoteNames = newNames
				fmt.Fprintf(stderr, "[gork] MCP %s tools reloaded: %d tool(s)\n", name, len(newNames))
			})
		}
		if initialized.Capabilities.Resources != nil {
			for _, adapter := range mcp.NewResourceAdapters(client, name) {
				if err := registry.Register(adapter); err != nil {
					closeClients()
					return nil, fmt.Errorf("register MCP resource tool from %q: %w", name, err)
				}
			}
		}
		if initialized.Capabilities.Prompts != nil {
			for _, adapter := range mcp.NewPromptAdapters(client, name) {
				if err := registry.Register(adapter); err != nil {
					closeClients()
					return nil, fmt.Errorf("register MCP prompt tool from %q: %w", name, err)
				}
			}
		}
		serverLabel := initialized.ServerInfo.Name
		if serverLabel == "" {
			serverLabel = name
		}
		fmt.Fprintf(stderr, "[gork] MCP %s ready: %d tool(s)\n", serverLabel, len(remoteTools))
	}
	return clients, nil
}

func startSubagentMCPServers(
	ctx context.Context,
	cfg config.Config,
	workspaceRoot string,
	registry *tools.Registry,
	approver tools.Approver,
	tokenProvider api.TokenProvider,
	stderr io.Writer,
	servers []mcp.ServerConfig,
) (func(), error) {
	cfg.MCPServers = make(map[string]config.MCPServerConfig, len(servers))
	for _, server := range servers {
		cfg.MCPServers[server.Name] = config.MCPServerConfig{
			Type: server.Type, Command: server.Command, Args: append([]string(nil), server.Args...),
			Env: cloneStringsMap(server.Env), URL: server.URL, Headers: cloneStringsMap(server.Headers),
		}
	}
	clients, err := startMCPServers(ctx, cfg, workspaceRoot, registry, approver, tokenProvider, stderr)
	if err != nil {
		return nil, err
	}
	return func() {
		for _, client := range clients {
			_ = client.Close()
		}
	}, nil
}

type sessionMCPRuntime struct {
	mu            sync.Mutex
	ctx           context.Context
	base          config.Config
	workspaceRoot string
	registry      *tools.Registry
	approver      tools.Approver
	tokenProvider api.TokenProvider
	stderr        io.Writer
	clients       []*mcp.Client
	clientConfigs []mcp.ServerConfig
	effective     []mcp.ServerConfig
	catalog       []mcp.ServerConfig
	closed        bool
}

func newSessionMCPRuntime(
	ctx context.Context,
	base config.Config,
	workspaceRoot string,
	registry *tools.Registry,
	approver tools.Approver,
	tokenProvider api.TokenProvider,
	stderr io.Writer,
) *sessionMCPRuntime {
	base.MCPServers = cloneMCPConfigMap(base.MCPServers)
	return &sessionMCPRuntime{
		ctx: ctx, base: base, workspaceRoot: workspaceRoot, registry: registry,
		approver: approver, tokenProvider: tokenProvider, stderr: stderr,
	}
}

func (r *sessionMCPRuntime) Update(ctx context.Context, requested []mcp.ServerConfig) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return errors.New("MCP runtime is closed")
	}
	previous := cloneMCPServerConfigs(r.clientConfigs)
	if err := r.restartLocked(requested); err == nil {
		r.clientConfigs = cloneMCPServerConfigs(requested)
		return nil
	} else if restoreErr := r.restartLocked(previous); restoreErr == nil {
		return err
	} else {
		return errors.Join(err, fmt.Errorf("restore previous MCP configuration: %w", restoreErr))
	}
}

func (r *sessionMCPRuntime) UpdateBase(ctx context.Context, base config.Config) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return errors.New("MCP runtime is closed")
	}
	previous := r.base
	base.MCPServers = cloneMCPConfigMap(base.MCPServers)
	r.base = base
	if err := r.restartLocked(r.clientConfigs); err == nil {
		return nil
	} else {
		r.base = previous
		if restoreErr := r.restartLocked(r.clientConfigs); restoreErr == nil {
			return err
		} else {
			return errors.Join(err, fmt.Errorf("restore previous MCP base configuration: %w", restoreErr))
		}
	}
}

func (r *sessionMCPRuntime) Configs() []mcp.ServerConfig {
	r.mu.Lock()
	defer r.mu.Unlock()
	return cloneMCPServerConfigs(r.effective)
}

func (r *sessionMCPRuntime) Catalog() []mcp.ServerConfig {
	r.mu.Lock()
	defer r.mu.Unlock()
	return cloneMCPServerConfigs(r.catalog)
}

func (r *sessionMCPRuntime) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	r.closed = true
	r.stopLocked()
}

func (r *sessionMCPRuntime) startLocked(requested []mcp.ServerConfig) ([]*mcp.Client, []mcp.ServerConfig, []mcp.ServerConfig, error) {
	cfg, effective, catalog := r.mergedConfig(requested)
	clients, err := startMCPServers(r.ctx, cfg, r.workspaceRoot, r.registry, r.approver, r.tokenProvider, r.stderr)
	return clients, effective, catalog, err
}

func (r *sessionMCPRuntime) restartLocked(requested []mcp.ServerConfig) error {
	r.stopLocked()
	clients, effective, catalog, err := r.startLocked(requested)
	if err != nil {
		r.stopLocked()
		return err
	}
	r.clients, r.effective, r.catalog = clients, effective, catalog
	return nil
}

func (r *sessionMCPRuntime) stopLocked() {
	names := registeredMCPToolNames(r.registry)
	if len(names) > 0 {
		_, _ = r.registry.Replace(names, nil)
	}
	for _, client := range r.clients {
		_ = client.Close()
	}
	r.clients = nil
	r.effective = nil
	r.catalog = nil
}

func (r *sessionMCPRuntime) mergedConfig(requested []mcp.ServerConfig) (config.Config, []mcp.ServerConfig, []mcp.ServerConfig) {
	cfg := r.base
	cfg.MCPServers = cloneMCPConfigMap(r.base.MCPServers)
	for _, server := range requested {
		cfg.MCPServers[server.Name] = config.MCPServerConfig{
			Type: server.Type, Command: server.Command, Args: append([]string(nil), server.Args...),
			Env: cloneStringsMap(server.Env), URL: server.URL, Headers: cloneStringsMap(server.Headers),
		}
	}
	for _, name := range cfg.DisabledMCPServers {
		if server, exists := cfg.MCPServers[name]; exists {
			disabled := false
			server.Enabled = &disabled
			cfg.MCPServers[name] = server
		}
	}
	names := make([]string, 0, len(cfg.MCPServers))
	for name := range cfg.MCPServers {
		names = append(names, name)
	}
	sort.Strings(names)
	effective := make([]mcp.ServerConfig, 0, len(names))
	catalog := make([]mcp.ServerConfig, 0, len(names))
	for _, name := range names {
		server := cfg.MCPServers[name]
		entry := mcp.ServerConfig{
			Type: server.Type, Name: name, Command: server.Command, Args: append([]string(nil), server.Args...),
			Env: cloneStringsMap(server.Env), URL: server.URL, Headers: cloneStringsMap(server.Headers), Disabled: !server.IsEnabled(),
			DisabledTools: append([]string(nil), cfg.DisabledMCPTools[name]...),
		}
		catalog = append(catalog, entry)
		if !entry.Disabled {
			effective = append(effective, entry)
		}
	}
	return cfg, effective, catalog
}

func registeredMCPToolNames(registry *tools.Registry) []string {
	var names []string
	for _, tool := range registry.SnapshotTools() {
		marker, ok := tool.(interface{ MCPServerName() string })
		if ok && marker.MCPServerName() != "" {
			names = append(names, tool.Definition().Name)
		}
	}
	return names
}

func cloneMCPConfigMap(source map[string]config.MCPServerConfig) map[string]config.MCPServerConfig {
	cloned := make(map[string]config.MCPServerConfig, len(source))
	for name, server := range source {
		server.Args = append([]string(nil), server.Args...)
		server.Env = cloneStringsMap(server.Env)
		server.Headers = cloneStringsMap(server.Headers)
		cloned[name] = server
	}
	return cloned
}

func watchMCPConfig(
	ctx context.Context,
	interval time.Duration,
	paths func() ([]string, error),
	reload func() error,
	stderr io.Writer,
) {
	currentPaths, err := paths()
	if err != nil {
		fmt.Fprintln(stderr, "[gork] MCP config watch failed:", err)
		return
	}
	fingerprint := fileSetFingerprint(currentPaths)
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			currentPaths, err := paths()
			if err != nil {
				fmt.Fprintln(stderr, "[gork] MCP config watch failed:", err)
				continue
			}
			next := fileSetFingerprint(currentPaths)
			if next == fingerprint {
				continue
			}
			if err := reload(); err != nil {
				fmt.Fprintln(stderr, "[gork] MCP config reload failed:", err)
				continue
			}
			fingerprint = next
			fmt.Fprintln(stderr, "[gork] MCP configuration reloaded")
		}
	}()
}

func watchModelConfig(ctx context.Context, interval time.Duration, paths []string, reload func() error, stderr io.Writer) {
	fingerprint := fileSetFingerprint(paths)
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			next := fileSetFingerprint(paths)
			if next == fingerprint {
				continue
			}
			if err := reload(); err != nil {
				fmt.Fprintln(stderr, "[gork] model config reload failed:", err)
				continue
			}
			fingerprint = next
			fmt.Fprintln(stderr, "[gork] model configuration reloaded")
		}
	}()
}

func fileSetFingerprint(paths []string) [sha256.Size]byte {
	hash := sha256.New()
	for _, path := range paths {
		_, _ = io.WriteString(hash, path)
		hash.Write([]byte{0})
		data, err := os.ReadFile(path)
		if err != nil {
			_, _ = io.WriteString(hash, err.Error())
		} else {
			hash.Write(data)
		}
		hash.Write([]byte{0})
	}
	var result [sha256.Size]byte
	copy(result[:], hash.Sum(nil))
	return result
}

func cloneMCPServerConfigs(source []mcp.ServerConfig) []mcp.ServerConfig {
	cloned := make([]mcp.ServerConfig, len(source))
	for index, server := range source {
		server.Args = append([]string(nil), server.Args...)
		server.Env = cloneStringsMap(server.Env)
		server.Headers = cloneStringsMap(server.Headers)
		server.DisabledTools = append([]string(nil), server.DisabledTools...)
		cloned[index] = server
	}
	return cloned
}

func cloneStringsMap(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	cloned := make(map[string]string, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

func mcpHTTPHeaders(server config.MCPServerConfig) map[string]string {
	headers := make(map[string]string, len(server.Headers)+1)
	for key, value := range server.Headers {
		headers[key] = value
	}
	if token := os.Getenv(server.BearerTokenEnvVar); server.BearerTokenEnvVar != "" && token != "" {
		for key := range headers {
			if strings.EqualFold(key, "Authorization") {
				delete(headers, key)
			}
		}
		headers["Authorization"] = "Bearer " + token
	}
	return headers
}

func newMCPSamplingHandler(cfg config.Config, approver tools.Approver, tokenProvider api.TokenProvider, serverName string) mcp.SamplingHandler {
	return func(ctx context.Context, request mcp.SamplingRequest) (mcp.SamplingResult, error) {
		if approver != nil {
			if err := approver.Approve(ctx, "MCP sampling", serverName); err != nil {
				return mcp.SamplingResult{}, err
			}
		}
		client, err := newModelClient(cfg, tokenProvider)
		if err != nil {
			return mcp.SamplingResult{}, err
		}
		return runMCPSampling(ctx, client, cfg.Model, request)
	}
}

func runMCPSampling(ctx context.Context, client agent.ResponseStreamer, model string, request mcp.SamplingRequest) (mcp.SamplingResult, error) {
	input := make([]api.InputItem, 0, len(request.Messages))
	for _, message := range request.Messages {
		if message.Role != "user" && message.Role != "assistant" {
			return mcp.SamplingResult{}, fmt.Errorf("unsupported sampling message role %q", message.Role)
		}
		var content any
		switch message.Content.Type {
		case "text":
			content = message.Content.Text
		case "image":
			if _, err := tools.DecodeImageAttachment(message.Content.MIMEType, message.Content.Data); err != nil {
				return mcp.SamplingResult{}, fmt.Errorf("invalid sampling image: %w", err)
			}
			content = []api.ContentPart{{
				Type: "input_image", ImageURL: "data:" + message.Content.MIMEType + ";base64," + message.Content.Data,
			}}
		default:
			return mcp.SamplingResult{}, fmt.Errorf("unsupported sampling content type %q", message.Content.Type)
		}
		input = append(input, api.InputItem{Type: "message", Role: message.Role, Content: content})
	}
	result, err := client.StreamResponse(ctx, api.ResponseRequest{
		Model: model, Instructions: request.SystemPrompt, Input: input,
		MaxOutputTokens: request.MaxTokens, Stream: true,
	}, nil)
	if err != nil {
		return mcp.SamplingResult{}, err
	}
	return mcp.SamplingResult{
		Role: "assistant", Content: mcp.SamplingContent{Type: "text", Text: result.Text},
		Model: model, StopReason: "endTurn",
	}, nil
}

func displayPath(path string) string {
	home, err := os.UserHomeDir()
	if err == nil {
		if rel, relErr := filepath.Rel(home, path); relErr == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return filepath.Join("~", rel)
		}
	}
	return path
}

func permissionRules(permission config.PermissionConfig, cliAllow, cliDeny []string) (allow, ask, deny []string, err error) {
	allow = append(allow, cliAllow...)
	deny = append(deny, cliDeny...)
	for index, rule := range permission.Rules {
		tool := strings.TrimSpace(rule.Tool)
		if tool == "" {
			tool = "any"
		}
		switch strings.ToLower(tool) {
		case "any":
			tool = "Any"
		case "bash":
			tool = "Bash"
		case "edit":
			tool = "Edit"
		case "mcp":
			tool = "Mcp"
		case "read":
			tool = "Read"
		case "grep":
			tool = "Grep"
		case "webfetch":
			tool = "WebFetch"
		default:
			return nil, nil, nil, fmt.Errorf("permission rule %d has unknown tool %q", index+1, rule.Tool)
		}
		mode := strings.ToLower(strings.TrimSpace(rule.PatternMode))
		if mode != "" && mode != "glob" && !(mode == "domain" && tool == "WebFetch") {
			return nil, nil, nil, fmt.Errorf("permission rule %d uses unsupported pattern_mode %q", index+1, rule.PatternMode)
		}
		if mode == "domain" {
			tool = "WebFetchDomain"
		}
		pattern := "*"
		if rule.Pattern != nil {
			pattern = *rule.Pattern
		}
		encoded := tool + "(" + pattern + ")"
		switch strings.ToLower(strings.TrimSpace(rule.Action)) {
		case "allow":
			allow = append(allow, encoded)
		case "ask":
			ask = append(ask, encoded)
		case "deny", "":
			deny = append(deny, encoded)
		default:
			return nil, nil, nil, fmt.Errorf("permission rule %d has unknown action %q", index+1, rule.Action)
		}
	}
	return allow, ask, deny, nil
}
