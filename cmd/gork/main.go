package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/lookcorner/go-cli/internal/acp"
	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/api"
	"github.com/lookcorner/go-cli/internal/config"
	"github.com/lookcorner/go-cli/internal/lsp"
	"github.com/lookcorner/go-cli/internal/mcp"
	"github.com/lookcorner/go-cli/internal/session"
	"github.com/lookcorner/go-cli/internal/skills"
	"github.com/lookcorner/go-cli/internal/tools"
	"github.com/lookcorner/go-cli/internal/tui"
	"github.com/lookcorner/go-cli/internal/workspace"
	worktrees "github.com/lookcorner/go-cli/internal/worktree"
)

const version = "0.1.0-dev"

type options struct {
	configPath  string
	workspace   string
	model       string
	baseURL     string
	backend     string
	system      string
	approval    string
	sessionDir  string
	maxSteps    int
	timeout     time.Duration
	showVersion bool
	interactive bool
	previousID  string
	resume      string
	tui         bool
	goal        bool
	goalRuns    int
	acp         bool
	allow       stringListFlag
	deny        stringListFlag
}

type stringListFlag []string

func (f *stringListFlag) String() string { return strings.Join(*f, ",") }
func (f *stringListFlag) Set(value string) error {
	*f = append(*f, value)
	return nil
}

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "gork:", err)
		os.Exit(1)
	}
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	var opts options
	flags := flag.NewFlagSet("gork", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&opts.configPath, "config", "", "path to JSON config file")
	flags.StringVar(&opts.workspace, "workspace", ".", "workspace directory")
	flags.StringVar(&opts.model, "model", "", "model ID (or GORK_MODEL)")
	flags.StringVar(&opts.baseURL, "base-url", "", "Responses-compatible API base URL")
	flags.StringVar(&opts.backend, "backend", "", "model API backend: responses, chat_completions, or anthropic_messages")
	flags.StringVar(&opts.system, "system", "", "additional agent instructions")
	flags.StringVar(&opts.approval, "approval", "prompt", "write/shell approval: prompt, auto, or deny")
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
	flags.Usage = func() {
		fmt.Fprintf(stderr, "Usage: gork [flags] [prompt]\n\n")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		return err
	}
	if opts.showVersion {
		fmt.Fprintln(stdout, version)
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
	if opts.model != "" {
		cfg.Model = opts.model
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
	allowRules, askRules, denyRules, err := permissionRules(cfg.Permission, opts.allow, opts.deny)
	if err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	if opts.acp {
		return runACP(cfg, opts, allowRules, askRules, denyRules, stdin, stdout, stderr)
	}

	inputReader := bufio.NewReader(stdin)
	prompt := strings.TrimSpace(strings.Join(flags.Args(), " "))
	if prompt == "" && !opts.interactive && !opts.tui {
		data, err := io.ReadAll(io.LimitReader(inputReader, 4<<20))
		if err != nil {
			return fmt.Errorf("read prompt: %w", err)
		}
		prompt = strings.TrimSpace(string(data))
	}
	if prompt == "" && !opts.interactive && !opts.tui {
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
	skillCatalog, err := skills.Discover(ws.Root(), skills.Config{
		Compat: cfg.Compat, Paths: cfg.Skills.Paths, Ignore: cfg.Skills.Ignore, Disabled: cfg.Skills.Disabled,
	})
	if err != nil {
		return err
	}
	cfg.SystemPrompt = joinInstructions(cfg.SystemPrompt, projectInstructions, skillCatalog.Summary())
	mode := tools.PermissionMode(opts.approval)
	if mode != tools.PermissionPrompt && mode != tools.PermissionAuto && mode != tools.PermissionDeny {
		return fmt.Errorf("invalid --approval %q", opts.approval)
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
		if err := logger.Append("session_metadata", sessionMetadata(context.Background(), ws.Root(), cfg.Model)); err != nil {
			return err
		}
	}
	defer logger.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if opts.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.timeout)
		defer cancel()
	}
	skillCatalog.Watch(ctx, time.Second)

	client, err := newModelClient(cfg)
	if err != nil {
		return err
	}
	var approver tools.Approver
	var askApprover tools.Approver
	var tuiBridge *tui.Bridge
	statusOutput := stderr
	if opts.tui {
		tuiBridge = tui.NewBridge(ctx, mode)
		defer tuiBridge.Close()
		approver = tuiBridge
		askApprover = tui.PromptApprover(tuiBridge)
		statusOutput = tuiBridge.StatusWriter()
	} else {
		approver = tools.PromptApprover{Mode: mode, Input: inputReader, Output: stderr}
		askApprover = tools.PromptApprover{Mode: tools.PermissionPrompt, Input: inputReader, Output: stderr}
	}
	approver, err = tools.NewPolicyApprover(approver, askApprover, allowRules, askRules, denyRules)
	if err != nil {
		return err
	}
	registry := tools.NewRegistry(ws, approver)
	artifactDir, err := session.ArtifactDir(logger.Path())
	if err != nil {
		return err
	}
	registry.ConfigureWebFetch(tools.WebFetchConfig{
		ArtifactDir: artifactDir, ContextWindow: cfg.ContextWindow,
		ProxyEndpoint: cfg.WebFetch.ProxyEndpoint, AllowedDomains: cfg.WebFetch.AllowedDomains,
		RestrictDomains: cfg.WebFetch.DomainsConfigured,
	})
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
	mcpClients, err := startMCPServers(ctx, cfg, ws.Root(), registry, approver, statusOutput)
	if err != nil {
		return err
	}
	defer func() {
		for _, mcpClient := range mcpClients {
			_ = mcpClient.Close()
		}
	}()
	lspManager, err := startLSPServers(ctx, cfg, ws, registry, statusOutput)
	if err != nil {
		return err
	}
	if lspManager != nil {
		defer lspManager.Close()
	}
	runner := &agent.Runner{
		Client: client, Tools: registry, Skills: skillCatalog, Logger: logger,
		Model: cfg.Model, Instructions: cfg.SystemPrompt, MaxSteps: cfg.MaxSteps,
		TextOutput: stdout, StatusOutput: stderr,
		ContextWindow: cfg.ContextWindow, CompactThresholdPercent: cfg.AutoCompactThresholdPercent,
	}
	if opts.tui {
		return tui.Run(ctx, runner, tuiBridge, prompt, opts.previousID, resumedTranscript, ws.Root(), cfg.Model)
	}
	fmt.Fprintf(stderr, "[gork] workspace: %s\n[gork] session: %s\n", ws.Root(), displayPath(logger.Path()))
	if opts.interactive {
		if resumedTranscript != "" {
			fmt.Fprintln(stdout, resumedTranscript)
		}
		return interactiveLoop(ctx, runner, inputReader, stdout, stderr, prompt, opts.previousID)
	}
	if opts.goal {
		if err := registry.BeginGoal(prompt); err != nil {
			return err
		}
		return goalLoop(ctx, runner, registry, stdout, stderr, prompt, opts.previousID, opts.goalRuns)
	}
	result, err := runner.RunTurn(ctx, prompt, opts.previousID)
	if err != nil {
		return err
	}
	if result.Text != "" && !strings.HasSuffix(result.Text, "\n") {
		fmt.Fprintln(stdout)
	}
	return nil
}

func newModelClient(cfg config.Config) (agent.ResponseStreamer, error) {
	httpClient := &http.Client{Timeout: cfg.HTTPTimeout}
	switch cfg.Backend {
	case "responses":
		return api.NewClient(cfg.BaseURL, cfg.APIKey, httpClient), nil
	case "chat_completions":
		client := api.NewChatClient(cfg.BaseURL, cfg.APIKey, httpClient)
		client.SetPruning(modelPruningConfig(cfg))
		return client, nil
	case "anthropic_messages":
		client := api.NewMessagesClient(cfg.BaseURL, cfg.APIKey, httpClient)
		client.SetPruning(modelPruningConfig(cfg))
		return client, nil
	default:
		return nil, fmt.Errorf("unsupported backend %q", cfg.Backend)
	}
}

func modelPruningConfig(cfg config.Config) api.PruningConfig {
	return api.PruningConfig{
		Enabled: cfg.Pruning.Enabled, KeepLastNTurns: cfg.Pruning.KeepLastNTurns,
		SoftTrimThreshold: cfg.Pruning.SoftTrimThreshold, SoftTrimHead: cfg.Pruning.SoftTrimHead,
		SoftTrimTail: cfg.Pruning.SoftTrimTail, HardClearAgeTurns: cfg.Pruning.HardClearAgeTurns,
	}
}

func runACP(cfg config.Config, opts options, allowRules, askRules, denyRules []string, stdin io.Reader, stdout, stderr io.Writer) error {
	mode := tools.PermissionMode(opts.approval)
	if mode != tools.PermissionPrompt && mode != tools.PermissionAuto && mode != tools.PermissionDeny {
		return fmt.Errorf("invalid --approval %q", opts.approval)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	server := &acp.Server{SessionDir: opts.sessionDir, Factory: func(
		sessionCtx context.Context,
		sessionConfig acp.SessionConfig,
		protocolApprover tools.Approver,
		textOutput io.Writer,
		statusOutput io.Writer,
	) (*agent.Runner, func(), error) {
		ws, err := workspace.Open(sessionConfig.CWD)
		if err != nil {
			return nil, nil, err
		}
		instructionFiles, err := ws.LoadInstructions(cfg.Compat)
		if err != nil {
			return nil, nil, err
		}
		catalog, err := skills.Discover(ws.Root(), skills.Config{
			Compat: cfg.Compat, Paths: cfg.Skills.Paths, Ignore: cfg.Skills.Ignore, Disabled: cfg.Skills.Disabled,
		})
		if err != nil {
			return nil, nil, err
		}
		instructions := joinInstructions(cfg.SystemPrompt, workspace.FormatInstructions(instructionFiles), catalog.Summary())
		approver := protocolApprover
		if mode != tools.PermissionPrompt {
			approver = tools.PromptApprover{Mode: mode}
		}
		approver, err = tools.NewPolicyApprover(approver, protocolApprover, allowRules, askRules, denyRules)
		if err != nil {
			return nil, nil, err
		}
		registry := tools.NewRegistry(ws, approver)
		if search, enabled := cfg.WebSearchEndpoint(); enabled {
			if err := registry.Register(tools.NewWebSearchTool(search.BaseURL, search.APIKey, search.Model, &http.Client{Timeout: cfg.HTTPTimeout})); err != nil {
				_ = registry.Close()
				return nil, nil, err
			}
		}
		readPolicy, err := tools.NewPolicyApprover(
			tools.PromptApprover{Mode: tools.PermissionAuto}, protocolApprover,
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
		if sessionConfig.ResumePath == "" {
			model := cfg.Model
			if sessionConfig.Model != "" {
				model = sessionConfig.Model
			}
			if err := logger.Append("session_metadata", sessionMetadata(ctx, ws.Root(), model)); err != nil {
				_ = logger.Close()
				_ = registry.Close()
				return nil, nil, err
			}
		}
		var mcpClients []*mcp.Client
		var lspManager *lsp.Manager
		cleanup := func() {
			if lspManager != nil {
				_ = lspManager.Close()
			}
			for _, client := range mcpClients {
				_ = client.Close()
			}
			_ = registry.Close()
			_ = logger.Close()
		}
		if catalog.Count() > 0 {
			if err := registry.Register(catalog.Tool()); err != nil {
				cleanup()
				return nil, nil, err
			}
		}
		sessionCfg := cfg
		if sessionConfig.Model != "" {
			sessionCfg.Model = sessionConfig.Model
		}
		sessionCfg.MCPServers = make(map[string]config.MCPServerConfig, len(cfg.MCPServers)+len(sessionConfig.MCPServers))
		for name, configured := range cfg.MCPServers {
			sessionCfg.MCPServers[name] = configured
		}
		for _, remote := range sessionConfig.MCPServers {
			sessionCfg.MCPServers[remote.Name] = config.MCPServerConfig{
				Type: remote.Type, Command: remote.Command, Args: remote.Args, Env: remote.Env,
				URL: remote.URL, Headers: remote.Headers,
			}
		}
		mcpClients, err = startMCPServers(sessionCtx, sessionCfg, ws.Root(), registry, approver, statusOutput)
		if err != nil {
			cleanup()
			return nil, nil, err
		}
		lspManager, err = startLSPServers(sessionCtx, cfg, ws, registry, statusOutput)
		if err != nil {
			cleanup()
			return nil, nil, err
		}
		modelClient, err := newModelClient(sessionCfg)
		if err != nil {
			cleanup()
			return nil, nil, err
		}
		watchCtx, stopSkills := context.WithCancel(sessionCtx)
		catalog.Watch(watchCtx, time.Second)
		var closeOnce sync.Once
		closeRuntime := func() {
			closeOnce.Do(func() {
				stopSkills()
				cleanup()
			})
		}
		return &agent.Runner{
			Client: modelClient, Tools: registry, Skills: catalog, Logger: logger,
			Model: sessionCfg.Model, Instructions: instructions, MaxSteps: cfg.MaxSteps,
			TextOutput: textOutput, StatusOutput: statusOutput,
			ContextWindow: cfg.ContextWindow, CompactThresholdPercent: cfg.AutoCompactThresholdPercent,
		}, closeRuntime, nil
	}}
	if err := server.Serve(ctx, stdin, stdout); err != nil {
		fmt.Fprintln(stderr, "[gork] ACP server failed:", err)
		return err
	}
	return nil
}

func sessionMetadata(ctx context.Context, cwd, model string) map[string]any {
	metadata := map[string]any{"cwd": cwd, "modelId": model}
	if head, err := worktrees.Head(ctx, cwd); err == nil && head != "" {
		metadata["headCommit"] = head
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
) error {
	prompt := objective
	for run := 1; run <= maxRuns; run++ {
		fmt.Fprintf(stderr, "[gork] goal run %d/%d\n", run, maxRuns)
		result, err := runner.RunTurn(ctx, prompt, previousResponseID)
		if err != nil {
			return err
		}
		previousResponseID = result.ResponseID
		if result.Text != "" && !strings.HasSuffix(result.Text, "\n") {
			fmt.Fprintln(stdout)
		}
		snapshot := registry.GoalSnapshot()
		switch snapshot.Status {
		case "completed":
			fmt.Fprintln(stderr, "[gork] goal completed:", snapshot.Message)
			return nil
		case "blocked":
			return fmt.Errorf("goal blocked: %s", snapshot.Message)
		}
		prompt = "Continue working toward the active goal. Verify the remaining work, then call update_goal with progress, completed=true only when fully achieved, or blocked_reason only if genuinely stuck."
	}
	return fmt.Errorf("goal remains active after %d runs", maxRuns)
}

func startLSPServers(
	ctx context.Context,
	cfg config.Config,
	ws *workspace.Workspace,
	registry *tools.Registry,
	stderr io.Writer,
) (*lsp.Manager, error) {
	names := make([]string, 0, len(cfg.LSPServers))
	for name, server := range cfg.LSPServers {
		if server.IsEnabled() {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil, nil
	}
	sort.Strings(names)
	manager := lsp.NewManager(ws)
	for _, name := range names {
		server := cfg.LSPServers[name]
		root := ws.Root()
		if server.WorkspaceFolder != "" {
			var err error
			root, err = ws.Resolve(server.WorkspaceFolder)
			if err != nil {
				_ = manager.Close()
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
			_ = manager.Close()
			return nil, err
		}
		if err := manager.Add(client); err != nil {
			_ = client.Close()
			_ = manager.Close()
			return nil, err
		}
		fmt.Fprintf(stderr, "[gork] LSP %s ready\n", name)
	}
	if err := registry.Register(manager.Tool()); err != nil {
		_ = manager.Close()
		return nil, fmt.Errorf("register LSP tool: %w", err)
	}
	return manager, nil
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
	input *bufio.Reader,
	stdout io.Writer,
	stderr io.Writer,
	initialPrompt string,
	previousResponseID string,
) error {
	fmt.Fprintln(stderr, "[gork] interactive mode; /exit to quit, /help for commands")
	prompt := strings.TrimSpace(initialPrompt)
	for {
		if prompt == "" {
			fmt.Fprint(stderr, "\ngork> ")
			line, err := input.ReadString('\n')
			if err != nil && !errors.Is(err, io.EOF) {
				return fmt.Errorf("read interactive prompt: %w", err)
			}
			prompt = strings.TrimSpace(line)
			if errors.Is(err, io.EOF) && prompt == "" {
				return nil
			}
		}
		switch prompt {
		case "":
			continue
		case "/exit", "/quit":
			return nil
		case "/help":
			fmt.Fprintln(stderr, "Commands: /compact, /help, /exit. Every other line is sent as a prompt.")
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
		}
		result, err := runner.RunTurn(ctx, prompt, previousResponseID)
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
	}
}

func startMCPServers(
	ctx context.Context,
	cfg config.Config,
	workspaceRoot string,
	registry *tools.Registry,
	approver tools.Approver,
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
		sampling := newMCPSamplingHandler(cfg, approver, name)
		fmt.Fprintf(stderr, "[gork] starting MCP server: %s\n", name)
		var client *mcp.Client
		var initialized mcp.InitializeResult
		var err error
		if server.URL != "" {
			httpConfig := mcp.HTTPConfig{Name: name, URL: server.URL, Headers: server.Headers}
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

func newMCPSamplingHandler(cfg config.Config, approver tools.Approver, serverName string) mcp.SamplingHandler {
	return func(ctx context.Context, request mcp.SamplingRequest) (mcp.SamplingResult, error) {
		if approver != nil {
			if err := approver.Approve(ctx, "MCP sampling", serverName); err != nil {
				return mcp.SamplingResult{}, err
			}
		}
		client, err := newModelClient(cfg)
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
