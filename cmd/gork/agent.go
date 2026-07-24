package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
)

func runAgent(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	normalized, err := normalizeAgentStdioArgs(args)
	if errors.Is(err, flag.ErrHelp) {
		fmt.Fprint(stderr, agentHelp)
		return nil
	}
	if err != nil {
		return err
	}
	return runOnce(normalized, stdin, stdout, stderr)
}

func normalizeAgentStdioArgs(args []string) ([]string, error) {
	result := []string{"--acp"}
	mode := ""
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
				return nil, errors.New("agent accepts one runtime mode")
			}
			mode = arg
		case "-m", "--model", "--reasoning-effort", "--effort", "--config", "--cwd", "--workspace",
			"--session-dir", "--allow", "--deny", "--permission-mode":
			value, err := next()
			if err != nil {
				return nil, err
			}
			result = append(result, arg, value)
		case "--always-approve", "--yolo":
			result = append(result, "--always-approve")
		case "--no-leader":
		case "--trust", "--disable-web-search", "--no-plan", "--no-subagents", "--no-ask-user",
			"--experimental-memory", "--no-memory":
			result = append(result, arg)
		case "-h", "--help":
			return nil, flag.ErrHelp
		case "--leader":
			return nil, errors.New("agent leader connection is not implemented")
		case "--reauth", "--reauthenticate", "--agent-profile", "--plugin-dir",
			"--grok-ws-origin", "--grok-ws-url", "--cli-chat-proxy-base-url", "--xai-api-base-url":
			return nil, fmt.Errorf("agent option %q is not implemented", cleanCLIText(arg))
		default:
			if agentValueOption(arg) {
				result = append(result, arg)
				continue
			}
			if strings.HasPrefix(arg, "-") {
				return nil, fmt.Errorf("unknown agent option %q", cleanCLIText(arg))
			}
			return nil, fmt.Errorf("unknown agent mode %q", cleanCLIText(arg))
		}
	}
	if mode == "" {
		return nil, errors.New("agent headless mode is not implemented; use `gork agent stdio` for ACP")
	}
	if mode != "stdio" {
		return nil, fmt.Errorf("agent %s mode is not implemented", mode)
	}
	return result, nil
}

func agentValueOption(arg string) bool {
	for _, name := range []string{
		"--model=", "--reasoning-effort=", "--effort=", "--config=", "--cwd=", "--workspace=",
		"--session-dir=", "--allow=", "--deny=", "--permission-mode=",
	} {
		if strings.HasPrefix(arg, name) && len(arg) > len(name) {
			return true
		}
	}
	return false
}

const agentHelp = `Usage: gork agent [options] stdio

Run the agent over Agent Client Protocol JSON-RPC stdio.

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
`
