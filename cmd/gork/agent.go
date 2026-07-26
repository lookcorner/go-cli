package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/lookcorner/go-cli/internal/leader"
	"github.com/lookcorner/go-cli/internal/version"
)

func runAgent(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	normalized, options, err := normalizeAgentArgs(args)
	if errors.Is(err, flag.ErrHelp) {
		fmt.Fprint(stderr, agentHelp)
		return nil
	}
	if err != nil {
		return err
	}
	usesLeader := agentUsesLeader(*options, normalized)
	if len(options.pluginDirs) > 0 && usesLeader {
		fmt.Fprintln(stderr, "gork: --plugin-dir is ignored in leader mode; run with --no-leader to load per-process plugins")
		options.pluginDirs = nil
		normalized = removeAgentPluginDirs(normalized)
	} else if len(options.pluginDirs) > 0 {
		normalized = removeAgentPluginDirs(normalized)
		options.pluginDirs = canonicalAgentPluginDirs(options.pluginDirs, stderr)
		for _, path := range options.pluginDirs {
			normalized = append(normalized, "--plugin-dir", path)
		}
	}
	switch options.mode {
	case "leader":
		return runAgentLeader(normalized, *options, stdin, stdout, stderr)
	case "serve":
		return runAgentServer(normalized, *options, stderr)
	}
	if options.forceLeader || (!options.noLeader && agentConfigUsesLeader(normalized)) {
		return runAgentFollower(normalized, stdin, stdout)
	}
	return runOnce(normalized, stdin, stdout, stderr)
}

func agentUsesLeader(options agentServerOptions, args []string) bool {
	return options.mode == "leader" || options.mode == "stdio" && (options.forceLeader || !options.noLeader && agentConfigUsesLeader(args))
}

type agentServerOptions struct {
	bind               string
	secret             string
	mode               string
	forceLeader        bool
	noLeader           bool
	noExitOnDisconnect bool
	pluginDirs         []string
}

func normalizeAgentArgs(args []string) ([]string, *agentServerOptions, error) {
	result := []string{"--acp"}
	mode := ""
	server := agentServerOptions{bind: "127.0.0.1:2419", secret: strings.TrimSpace(os.Getenv("GROK_AGENT_SECRET"))}
	serverOptionSet := false
	for index := 0; index < len(args); index++ {
		arg := args[index]
		next := func() (string, error) {
			index++
			if index >= len(args) {
				return "", fmt.Errorf("%s requires a value", arg)
			}
			return args[index], nil
		}
		switch arg {
		case "stdio", "headless", "serve", "leader":
			if mode != "" {
				return nil, nil, errors.New("agent accepts one runtime mode")
			}
			mode = arg
		case "-m", "--model", "--reasoning-effort", "--effort", "--config", "--cwd", "--workspace",
			"--session-dir", "--allow", "--deny", "--permission-mode", "--plugin-dir",
			"--cli-chat-proxy-base-url", "--xai-api-base-url":
			value, err := next()
			if err != nil {
				return nil, nil, err
			}
			switch arg {
			case "--plugin-dir":
				server.pluginDirs = append(server.pluginDirs, value)
				result = append(result, arg, value)
			case "--xai-api-base-url":
				result = append(result, "--base-url", value)
			default:
				result = append(result, arg, value)
			}
		case "--bind", "--secret":
			serverOptionSet = true
			value, err := next()
			if err != nil {
				return nil, nil, err
			}
			if arg == "--bind" {
				server.bind = value
			} else {
				server.secret = value
			}
		case "--always-approve", "--yolo":
			result = append(result, "--always-approve")
		case "--no-leader":
			server.noLeader = true
		case "--no-exit-on-disconnect":
			server.noExitOnDisconnect = true
		case "--trust", "--disable-web-search", "--no-plan", "--no-subagents", "--no-ask-user",
			"--experimental-memory", "--no-memory":
			result = append(result, arg)
		case "-h", "--help":
			return nil, nil, flag.ErrHelp
		case "--leader":
			server.forceLeader = true
		case "--reauth", "--reauthenticate", "--agent-profile",
			"--grok-ws-origin", "--grok-ws-url":
			return nil, nil, fmt.Errorf("agent option %q is not implemented", cleanCLIText(arg))
		default:
			if agentValueOption(arg) {
				if value, ok := strings.CutPrefix(arg, "--xai-api-base-url="); ok {
					result = append(result, "--base-url="+value)
					continue
				}
				if strings.HasPrefix(arg, "--bind=") {
					serverOptionSet = true
					server.bind = strings.TrimPrefix(arg, "--bind=")
					continue
				}
				if strings.HasPrefix(arg, "--secret=") {
					serverOptionSet = true
					server.secret = strings.TrimPrefix(arg, "--secret=")
					continue
				}
				if value, ok := strings.CutPrefix(arg, "--plugin-dir="); ok {
					server.pluginDirs = append(server.pluginDirs, value)
				}
				result = append(result, arg)
				continue
			}
			if strings.HasPrefix(arg, "-") {
				return nil, nil, fmt.Errorf("unknown agent option %q", cleanCLIText(arg))
			}
			return nil, nil, fmt.Errorf("unknown agent mode %q", cleanCLIText(arg))
		}
	}
	if mode == "" {
		return nil, nil, errors.New("agent headless mode is not implemented; use `gork agent stdio` for ACP")
	}
	if mode == "headless" {
		return nil, nil, fmt.Errorf("agent %s mode is not implemented", mode)
	}
	if server.forceLeader && server.noLeader {
		return nil, nil, errors.New("--leader and --no-leader cannot be used together")
	}
	server.mode = mode
	if mode == "leader" {
		if serverOptionSet {
			return nil, nil, errors.New("--bind and --secret require agent serve")
		}
		if server.forceLeader || server.noLeader {
			return nil, nil, errors.New("--leader and --no-leader require agent stdio")
		}
		return result, &server, nil
	}
	if server.noExitOnDisconnect {
		return nil, nil, errors.New("--no-exit-on-disconnect requires agent leader")
	}
	if mode == "serve" {
		if strings.TrimSpace(server.bind) == "" {
			return nil, nil, errors.New("--bind cannot be empty")
		}
		return result, &server, nil
	}
	if serverOptionSet {
		return nil, nil, errors.New("--bind and --secret require agent serve")
	}
	return result, &server, nil
}

func runAgentLeader(args []string, options agentServerOptions, stdin io.Reader, stdout, stderr io.Writer) error {
	opts, _, err := parseRunOptions(args, io.Discard)
	if err != nil {
		return err
	}
	root, err := agentLeaderRoot(opts.configPath)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	server, err := leader.Start(ctx, leader.ServerConfig{
		Root: root, SocketPath: leaderSocketPath(root),
		BinaryVersion:      version.Current,
		NoExitOnDisconnect: options.noExitOnDisconnect,
	})
	if err != nil {
		return err
	}
	defer server.Close()
	fmt.Fprintf(stderr, "[gork] leader listening on %s\n", server.SocketPath())
	runErr := runOnce(args, server, server, stderr)
	closeErr := server.Close()
	return errors.Join(runErr, closeErr, server.Wait())
}

func agentValueOption(arg string) bool {
	for _, name := range []string{
		"--model=", "--reasoning-effort=", "--effort=", "--config=", "--cwd=", "--workspace=",
		"--session-dir=", "--allow=", "--deny=", "--permission-mode=", "--bind=", "--secret=",
		"--plugin-dir=", "--cli-chat-proxy-base-url=", "--xai-api-base-url=",
	} {
		if strings.HasPrefix(arg, name) && len(arg) > len(name) {
			return true
		}
	}
	return false
}

func canonicalAgentPluginDirs(paths []string, output io.Writer) []string {
	result := make([]string, 0, len(paths))
	for _, path := range paths {
		canonical, err := filepath.Abs(path)
		if err == nil {
			canonical, err = filepath.EvalSymlinks(canonical)
		}
		if err != nil {
			fmt.Fprintf(output, "gork: --plugin-dir %s: %v; skipping\n", path, err)
			continue
		}
		info, err := os.Stat(canonical)
		if err != nil || !info.IsDir() {
			fmt.Fprintf(output, "gork: --plugin-dir %s: not a directory; skipping\n", path)
			continue
		}
		result = append(result, canonical)
	}
	return result
}

func removeAgentPluginDirs(args []string) []string {
	result := make([]string, 0, len(args))
	for index := 0; index < len(args); index++ {
		if args[index] == "--plugin-dir" {
			index++
			continue
		}
		if strings.HasPrefix(args[index], "--plugin-dir=") {
			continue
		}
		result = append(result, args[index])
	}
	return result
}

const agentHelp = `Usage: gork agent [options] <stdio|serve|leader>

Run the agent over Agent Client Protocol JSON-RPC stdio or WebSocket.

Supported options:
  -m, --model MODEL
      --reasoning-effort EFFORT
      --always-approve, --yolo
      --leader
      --no-leader
      --config PATH
      --cwd DIR, --workspace DIR
      --session-dir DIR
      --plugin-dir DIR      load a trusted plugin for this process (repeatable)
      --cli-chat-proxy-base-url URL
      --xai-api-base-url URL
      --trust
      --disable-web-search
      --no-plan, --no-subagents, --no-ask-user
      --experimental-memory, --no-memory
      --allow RULE, --deny RULE
      --permission-mode MODE

Serve options:
      --bind ADDRESS       listen address (default 127.0.0.1:2419)
      --secret TOKEN       bearer or server-key token (or GROK_AGENT_SECRET)

Leader options:
      --no-exit-on-disconnect
`
