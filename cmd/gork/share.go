package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/lookcorner/go-cli/internal/auth"
	"github.com/lookcorner/go-cli/internal/config"
	sessionshare "github.com/lookcorner/go-cli/internal/share"
)

var (
	fetchShareSettings = config.FetchRemoteSettingsForSession
	shareSession       = func(ctx context.Context, service sessionshare.Service, sessionID string) (string, error) {
		return service.Share(ctx, sessionID)
	}
)

func runShare(args []string, stdout, stderr io.Writer) error {
	sessionID, configPath, sessionDir, help, err := parseShareArgs(args)
	if err != nil {
		return err
	}
	if help {
		fmt.Fprintln(stdout, shareUsage)
		return nil
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	if err := prepareManagedPolicy(&cfg, configPath, stderr); err != nil {
		return err
	}
	authPath, err := auth.DefaultPath()
	if err != nil {
		return err
	}
	authConfig := auth.DefaultConfig()
	applyAuthPolicy(&authConfig, cfg)
	credential, err := auth.Load(authPath, authConfig.Scope())
	if err != nil || !credential.IsXAIAuth() {
		return errors.New("share requires an xAI login; run `gork login`")
	}
	tokenProvider := newAuthTokenProvider(cfg, authPath, authConfig, stderr)
	token := credential.Key
	if refreshed, refreshErr := tokenProvider(context.Background(), ""); refreshErr == nil && refreshed != "" {
		token = refreshed
	}
	settingsCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	remote := fetchShareSettings(settingsCtx, cfg.ProxyBaseURL, token, credential.UserID, credential.Email, &http.Client{Timeout: 3 * time.Second})
	cancel()
	cfg.ApplyRemoteSettings(remote)

	service := newShareService(cfg, tokenProvider, sessionDir, func() bool { return cfg.SharingEnabled })
	url, err := shareSession(context.Background(), service, sessionID)
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, url)
	return nil
}

const shareUsage = "Usage: gork share <session-id> [--config path] [--session-dir path]"

func parseShareArgs(args []string) (sessionID, configPath, sessionDir string, help bool, err error) {
	var positionals []string
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
		case "--config":
			configPath, err = next()
		case "--session-dir":
			sessionDir, err = next()
		case "-h", "--help":
			return "", "", "", true, nil
		default:
			if strings.HasPrefix(arg, "-") {
				return "", "", "", false, fmt.Errorf("unknown share option %q", cleanCLIText(arg))
			}
			positionals = append(positionals, arg)
		}
		if err != nil {
			return "", "", "", false, err
		}
	}
	if len(positionals) != 1 {
		return "", "", "", false, errors.New(shareUsage)
	}
	return positionals[0], configPath, sessionDir, false, nil
}
