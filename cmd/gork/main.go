package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/api"
	"github.com/lookcorner/go-cli/internal/config"
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

	prompt := strings.TrimSpace(strings.Join(flags.Args(), " "))
	if prompt == "" {
		data, err := io.ReadAll(io.LimitReader(stdin, 4<<20))
		if err != nil {
			return fmt.Errorf("read prompt: %w", err)
		}
		prompt = strings.TrimSpace(string(data))
	}
	if prompt == "" {
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
	registry := tools.NewRegistry(ws, tools.PromptApprover{Mode: mode, Input: stdin, Output: stderr})
	runner := &agent.Runner{
		Client: client, Tools: registry, Logger: logger,
		Model: cfg.Model, Instructions: cfg.SystemPrompt, MaxSteps: cfg.MaxSteps,
		TextOutput: stdout, StatusOutput: stderr,
	}
	fmt.Fprintf(stderr, "[gork] workspace: %s\n[gork] session: %s\n", ws.Root(), displayPath(logger.Path()))
	result, err := runner.Run(ctx, prompt)
	if err != nil {
		return err
	}
	if result.Text != "" && !strings.HasSuffix(result.Text, "\n") {
		fmt.Fprintln(stdout)
	}
	return nil
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
