package main

import (
	"context"
	"io"
	"path/filepath"
	"time"

	"github.com/lookcorner/go-cli/internal/config"
	"github.com/lookcorner/go-cli/internal/leader"
)

var spawnAgentLeader = startAgentLeader

func runAgentFollower(args []string, stdin io.Reader, stdout io.Writer) error {
	opts, _, err := parseRunOptions(args, io.Discard)
	if err != nil {
		return err
	}
	root, err := agentLeaderRoot(opts.configPath)
	if err != nil {
		return err
	}
	registration := leader.Registration{
		ClientType: "grok-cli",
		Capabilities: leader.ClientCapabilities{
			YoloMode: opts.alwaysApprove, AutoMode: opts.approval == "auto",
			DefaultModel: opts.model, CodeNav: true, Terminal: true,
			FSRead: true, FSWrite: opts.approval != "deny",
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client, err := leader.ConnectOrSpawn(ctx, leaderSocketPath(root), registration, func() error {
		return spawnAgentLeader(root, args)
	})
	if err != nil {
		return err
	}
	defer client.Close()
	return client.Serve(context.Background(), stdin, stdout)
}

func agentConfigUsesLeader(args []string) bool {
	opts, _, err := parseRunOptions(args, io.Discard)
	if err != nil {
		return false
	}
	cfg, err := config.Load(opts.configPath)
	return err == nil && cfg.UseLeader
}

func agentLeaderRoot(configPath string) (string, error) {
	if configPath == "" {
		path, err := config.DefaultPath()
		if err != nil {
			return "", err
		}
		return filepath.Dir(path), nil
	}
	absolute, err := filepath.Abs(configPath)
	if err != nil {
		return "", err
	}
	return filepath.Dir(absolute), nil
}

func leaderSocketPath(root string) string {
	return leader.SocketPath(root)
}
