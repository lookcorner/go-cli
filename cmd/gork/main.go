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
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/lookcorner/go-cli/internal/acp"
	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/api"
	"github.com/lookcorner/go-cli/internal/auth"
	"github.com/lookcorner/go-cli/internal/config"
	"github.com/lookcorner/go-cli/internal/lsp"
	"github.com/lookcorner/go-cli/internal/mcp"
	"github.com/lookcorner/go-cli/internal/plugin"
	"github.com/lookcorner/go-cli/internal/session"
	"github.com/lookcorner/go-cli/internal/skills"
	"github.com/lookcorner/go-cli/internal/tools"
	"github.com/lookcorner/go-cli/internal/tui"
	"github.com/lookcorner/go-cli/internal/version"
	"github.com/lookcorner/go-cli/internal/workspace"
	worktrees "github.com/lookcorner/go-cli/internal/worktree"
)

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
	trust       bool
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
	if len(args) > 0 && args[0] == "login" {
		return runLogin(args[1:], stdin, stdout, stderr)
	}
	if len(args) > 0 && args[0] == "logout" {
		return runLogout(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "setup" {
		return runSetup(args[1:], stdout, stderr)
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
	flags.BoolVar(&opts.trust, "trust", false, "trust this workspace's executable project configuration")
	flags.Usage = func() {
		fmt.Fprintf(stderr, "Usage: gork [flags] [prompt]\n       gork login [--oauth|--device-auth]\n       gork logout\n       gork setup\n\n")
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

	client, err := newModelClient(cfg, tokenProvider)
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
	if err := registry.ConfigureHunkState(artifactDir); err != nil {
		_ = registry.Close()
		return err
	}
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
	runner := &agent.Runner{
		Client: client, Tools: registry, Skills: skillCatalog, Logger: logger,
		SessionID: logger.ID(),
		Model:     cfg.Model, Instructions: cfg.SystemPrompt, MaxSteps: cfg.MaxSteps,
		TextOutput: stdout, StatusOutput: stderr,
		ContextWindow: cfg.ContextWindow, CompactThresholdPercent: cfg.AutoCompactThresholdPercent,
		UpdateMCPServers: mcpRuntime.Update, MCPServers: mcpRuntime.Configs,
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

func modelPruningConfig(cfg config.Config) api.PruningConfig {
	return api.PruningConfig{
		Enabled: cfg.Pruning.Enabled, KeepLastNTurns: cfg.Pruning.KeepLastNTurns,
		SoftTrimThreshold: cfg.Pruning.SoftTrimThreshold, SoftTrimHead: cfg.Pruning.SoftTrimHead,
		SoftTrimTail: cfg.Pruning.SoftTrimTail, HardClearAgeTurns: cfg.Pruning.HardClearAgeTurns,
	}
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
}

func runACP(cfg config.Config, opts options, allowRules, askRules, denyRules []string, tokenProvider api.TokenProvider, stdin io.Reader, stdout, stderr io.Writer) error {
	mode := tools.PermissionMode(opts.approval)
	if mode != tools.PermissionPrompt && mode != tools.PermissionAuto && mode != tools.PermissionDeny {
		return fmt.Errorf("invalid --approval %q", opts.approval)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var extensionsMu sync.Mutex
	dynamicSkills := cloneSkillsConfig(cfg.Skills)
	dynamicPlugins := clonePluginsConfig(cfg.Plugins)
	pluginStates := make(map[*sessionPluginState]bool)
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
		var mcpRuntime *sessionMCPRuntime
		var lspManager *lsp.Manager
		cleanup := func() {
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
		if sessionConfig.Model != "" {
			sessionCfg.Model = sessionConfig.Model
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
		watchCtx, stopSkills := context.WithCancel(sessionCtx)
		catalog.Watch(watchCtx, time.Second)
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
		extensionsMu.Lock()
		pluginStates[pluginState] = true
		extensionsMu.Unlock()
		watchMCPConfig(sessionCtx, time.Second, func() ([]string, error) {
			extensionsMu.Lock()
			defer extensionsMu.Unlock()
			return config.MCPWatchPaths(pluginState.root, opts.configPath, pluginState.mcpSource, pluginState.inventory, pluginState.trusted), nil
		}, func() error {
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
			source.Compat = reloaded.Compat
			base := source
			base.MCPServers = config.DiscoverMCPServers(pluginState.root, base, enabledPlugins(inventory), pluginState.trusted)
			if err := pluginState.mcp.UpdateBase(sessionCtx, base); err != nil {
				return err
			}
			extensionsMu.Lock()
			pluginState.mcpSource.MCPServers = reloaded.MCPServers
			pluginState.mcpSource.Compat = reloaded.Compat
			extensionsMu.Unlock()
			return nil
		}, statusOutput)
		var closeOnce sync.Once
		closeRuntime := func() {
			closeOnce.Do(func() {
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
				extensionsMu.Lock()
				previousInventory := append([]plugin.Plugin(nil), state.inventory...)
				mcpSource := state.mcpSource
				extensionsMu.Unlock()
				inventory, err := plugin.Inventory(state.root, plugin.Config{
					Paths: settings.Paths, Enabled: settings.Enabled, Disabled: settings.Disabled,
					ProjectTrusted: state.trusted,
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
				mcpBase.MCPServers = config.DiscoverMCPServers(state.root, mcpBase, enabledPlugins(inventory), state.trusted)
				if err := state.mcp.UpdateBase(updateCtx, mcpBase); err != nil {
					rollbackErr := state.catalog.ReconfigurePlugins(enabledPlugins(previousInventory))
					state.updateMu.Unlock()
					if rollbackErr != nil {
						return nil, errors.Join(err, fmt.Errorf("restore previous plugin catalog: %w", rollbackErr))
					}
					return nil, err
				}
				lspBase := state.lspSource
				lspBase.LSPServers = config.DiscoverLSPServers(state.root, lspBase, enabledPlugins(inventory), state.trusted)
				if err := state.updateLSP(lspBase); err != nil {
					var rollbackErr error
					if catalogErr := state.catalog.ReconfigurePlugins(enabledPlugins(previousInventory)); catalogErr != nil {
						rollbackErr = errors.Join(rollbackErr, catalogErr)
					}
					previousMCP := mcpSource
					previousMCP.MCPServers = config.DiscoverMCPServers(state.root, previousMCP, enabledPlugins(previousInventory), state.trusted)
					if mcpErr := state.mcp.UpdateBase(updateCtx, previousMCP); mcpErr != nil {
						rollbackErr = errors.Join(rollbackErr, mcpErr)
					}
					state.updateMu.Unlock()
					return nil, errors.Join(err, rollbackErr)
				}
				extensionsMu.Lock()
				state.inventory = append([]plugin.Plugin(nil), inventory...)
				extensionsMu.Unlock()
				state.updateMu.Unlock()
			}
			return pluginInventory(), nil
		}
		return &agent.Runner{
			Client: modelClient, Tools: registry, Skills: catalog, PluginInventory: pluginInventory, Logger: logger,
			Model: sessionCfg.Model, Instructions: instructions, MaxSteps: cfg.MaxSteps,
			TextOutput: textOutput, StatusOutput: statusOutput,
			ContextWindow: cfg.ContextWindow, CompactThresholdPercent: cfg.AutoCompactThresholdPercent,
			UpdateMCPServers: mcpRuntime.Update, MCPServers: mcpRuntime.Configs,
			UpdateSkills:  updateSkills,
			UpdatePlugins: updatePlugins,
		}, closeRuntime, nil
	}}
	if err := server.Serve(ctx, stdin, stdout); err != nil {
		fmt.Fprintln(stderr, "[gork] ACP server failed:", err)
		return err
	}
	return nil
}

func cloneSkillsConfig(source config.SkillsConfig) config.SkillsConfig {
	return config.SkillsConfig{
		Paths: append([]string(nil), source.Paths...), Ignore: append([]string(nil), source.Ignore...),
		Disabled: append([]string(nil), source.Disabled...),
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
	catalog, err := skills.Discover(root, skills.Config{
		Compat: cfg.Compat, Paths: cfg.Skills.Paths, Ignore: cfg.Skills.Ignore,
		Disabled: cfg.Skills.Disabled, Plugins: plugins,
	})
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

func (r *sessionMCPRuntime) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	r.closed = true
	r.stopLocked()
}

func (r *sessionMCPRuntime) startLocked(requested []mcp.ServerConfig) ([]*mcp.Client, []mcp.ServerConfig, error) {
	cfg, effective := r.mergedConfig(requested)
	clients, err := startMCPServers(r.ctx, cfg, r.workspaceRoot, r.registry, r.approver, r.tokenProvider, r.stderr)
	return clients, effective, err
}

func (r *sessionMCPRuntime) restartLocked(requested []mcp.ServerConfig) error {
	r.stopLocked()
	clients, effective, err := r.startLocked(requested)
	if err != nil {
		r.stopLocked()
		return err
	}
	r.clients, r.effective = clients, effective
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
}

func (r *sessionMCPRuntime) mergedConfig(requested []mcp.ServerConfig) (config.Config, []mcp.ServerConfig) {
	cfg := r.base
	cfg.MCPServers = cloneMCPConfigMap(r.base.MCPServers)
	for _, server := range requested {
		cfg.MCPServers[server.Name] = config.MCPServerConfig{
			Type: server.Type, Command: server.Command, Args: append([]string(nil), server.Args...),
			Env: cloneStringsMap(server.Env), URL: server.URL, Headers: cloneStringsMap(server.Headers),
		}
	}
	names := make([]string, 0, len(cfg.MCPServers))
	for name, server := range cfg.MCPServers {
		if server.IsEnabled() {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	effective := make([]mcp.ServerConfig, 0, len(names))
	for _, name := range names {
		server := cfg.MCPServers[name]
		effective = append(effective, mcp.ServerConfig{
			Type: server.Type, Name: name, Command: server.Command, Args: append([]string(nil), server.Args...),
			Env: cloneStringsMap(server.Env), URL: server.URL, Headers: cloneStringsMap(server.Headers),
		})
	}
	return cfg, effective
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
