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

	"github.com/lookcorner/go-cli/internal/config"
	"github.com/lookcorner/go-cli/internal/leader"
	"github.com/lookcorner/go-cli/internal/version"
)

func runAgent(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	normalized, serve, err := normalizeAgentArgs(args)
	if errors.Is(err, flag.ErrHelp) {
		fmt.Fprint(stderr, agentHelp)
		return nil
	}
	if err != nil {
		return err
	}
	if serve != nil {
		if serve.leader {
			return runAgentLeader(normalized, *serve, stdin, stdout, stderr)
		}
		return runAgentServer(normalized, *serve, stderr)
	}
	return runOnce(normalized, stdin, stdout, stderr)
}

type agentServerOptions struct {
	bind               string
	secret             string
	leader             bool
	noExitOnDisconnect bool
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
			"--session-dir", "--allow", "--deny", "--permission-mode":
			value, err := next()
			if err != nil {
				return nil, nil, err
			}
			result = append(result, arg, value)
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
		case "--no-exit-on-disconnect":
			server.noExitOnDisconnect = true
		case "--trust", "--disable-web-search", "--no-plan", "--no-subagents", "--no-ask-user",
			"--experimental-memory", "--no-memory":
			result = append(result, arg)
		case "-h", "--help":
			return nil, nil, flag.ErrHelp
		case "--leader":
			return nil, nil, errors.New("agent leader connection is not implemented")
		case "--reauth", "--reauthenticate", "--agent-profile", "--plugin-dir",
			"--grok-ws-origin", "--grok-ws-url", "--cli-chat-proxy-base-url", "--xai-api-base-url":
			return nil, nil, fmt.Errorf("agent option %q is not implemented", cleanCLIText(arg))
		default:
			if agentValueOption(arg) {
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
	if mode == "leader" {
		if serverOptionSet {
			return nil, nil, errors.New("--bind and --secret require agent serve")
		}
		server.leader = true
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
	return result, nil, nil
}

func runAgentLeader(args []string, options agentServerOptions, stdin io.Reader, stdout, stderr io.Writer) error {
	configPath, err := config.DefaultPath()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	server, err := leader.Start(ctx, leader.ServerConfig{
		Root: filepath.Dir(configPath), BinaryVersion: version.Current,
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
	} {
		if strings.HasPrefix(arg, name) && len(arg) > len(name) {
			return true
		}
	}
	return false
}

const agentHelp = `Usage: gork agent [options] <stdio|serve|leader>

Run the agent over Agent Client Protocol JSON-RPC stdio or WebSocket.

Supported options:
  -m, --model MODEL
      --reasoning-effort EFFORT
      --always-approve, --yolo
      --no-leader
      --config PATH
      --cwd DIR, --workspace DIR
      --session-dir DIR
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
