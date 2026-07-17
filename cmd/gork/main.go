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
	"syscall"
	"time"

	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/api"
	"github.com/lookcorner/go-cli/internal/config"
	"github.com/lookcorner/go-cli/internal/mcp"
	"github.com/lookcorner/go-cli/internal/session"
	"github.com/lookcorner/go-cli/internal/tools"
	"github.com/lookcorner/go-cli/internal/workspace"
)

const version = "0.1.0-dev"

type options struct {
	configPath  string
	workspace   string
	model       string
	baseURL     string
	system      string
	approval    string
	sessionDir  string
	maxSteps    int
	timeout     time.Duration
	showVersion bool
	interactive bool
	previousID  string
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
	flags.StringVar(&opts.system, "system", "", "additional agent instructions")
	flags.StringVar(&opts.approval, "approval", "prompt", "write/shell approval: prompt, auto, or deny")
	flags.StringVar(&opts.sessionDir, "session-dir", "", "session JSONL directory")
	flags.IntVar(&opts.maxSteps, "max-steps", 0, "maximum model/tool iterations")
	flags.DurationVar(&opts.timeout, "timeout", 0, "overall run timeout")
	flags.BoolVar(&opts.showVersion, "version", false, "print version")
	flags.BoolVar(&opts.interactive, "interactive", false, "start an interactive multi-turn session")
	flags.StringVar(&opts.previousID, "previous-response-id", "", "continue a stored Responses API conversation")
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
	if opts.system != "" {
		cfg.SystemPrompt = opts.system
	}
	if opts.maxSteps > 0 {
		cfg.MaxSteps = opts.maxSteps
	}
	if err := cfg.Validate(); err != nil {
		return err
	}

	inputReader := bufio.NewReader(stdin)
	prompt := strings.TrimSpace(strings.Join(flags.Args(), " "))
	if prompt == "" && !opts.interactive {
		data, err := io.ReadAll(io.LimitReader(inputReader, 4<<20))
		if err != nil {
			return fmt.Errorf("read prompt: %w", err)
		}
		prompt = strings.TrimSpace(string(data))
	}
	if prompt == "" && !opts.interactive {
		flags.Usage()
		return errors.New("prompt is required as arguments or stdin")
	}

	ws, err := workspace.Open(opts.workspace)
	if err != nil {
		return err
	}
	mode := tools.PermissionMode(opts.approval)
	if mode != tools.PermissionPrompt && mode != tools.PermissionAuto && mode != tools.PermissionDeny {
		return fmt.Errorf("invalid --approval %q", opts.approval)
	}
	logger, err := session.NewLogger(opts.sessionDir)
	if err != nil {
		return err
	}
	defer logger.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if opts.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.timeout)
		defer cancel()
	}

	httpClient := &http.Client{Timeout: cfg.HTTPTimeout}
	client := api.NewClient(cfg.BaseURL, cfg.APIKey, httpClient)
	approver := tools.PromptApprover{Mode: mode, Input: inputReader, Output: stderr}
	registry := tools.NewRegistry(ws, approver)
	mcpClients, err := startMCPServers(ctx, cfg, ws.Root(), registry, approver, stderr)
	if err != nil {
		return err
	}
	defer func() {
		for _, mcpClient := range mcpClients {
			_ = mcpClient.Close()
		}
	}()
	runner := &agent.Runner{
		Client: client, Tools: registry, Logger: logger,
		Model: cfg.Model, Instructions: cfg.SystemPrompt, MaxSteps: cfg.MaxSteps,
		TextOutput: stdout, StatusOutput: stderr,
	}
	fmt.Fprintf(stderr, "[gork] workspace: %s\n[gork] session: %s\n", ws.Root(), displayPath(logger.Path()))
	if opts.interactive {
		return interactiveLoop(ctx, runner, inputReader, stdout, stderr, prompt, opts.previousID)
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
			fmt.Fprintln(stderr, "Commands: /help, /exit. Every other line is sent as a prompt.")
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
		fmt.Fprintf(stderr, "[gork] starting MCP server: %s\n", name)
		client, initialized, err := mcp.Start(ctx, mcp.ProcessConfig{
			Name: name, Command: server.Command, Args: server.Args,
			Env: server.Env, Dir: workspaceRoot, Stderr: stderr,
		})
		if err != nil {
			closeClients()
			return nil, err
		}
		clients = append(clients, client)
		listCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		remoteTools, err := client.ListTools(listCtx)
		cancel()
		if err != nil {
			closeClients()
			return nil, fmt.Errorf("list tools from MCP server %q: %w", name, err)
		}
		for _, adapter := range mcp.NewToolAdapters(client, name, remoteTools, approver) {
			if err := registry.Register(adapter); err != nil {
				closeClients()
				return nil, fmt.Errorf("register MCP tool from %q: %w", name, err)
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

func displayPath(path string) string {
	home, err := os.UserHomeDir()
	if err == nil {
		if rel, relErr := filepath.Rel(home, path); relErr == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return filepath.Join("~", rel)
		}
	}
	return path
}
