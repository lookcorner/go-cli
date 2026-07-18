package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

type ExternalProvider struct {
	Command      string
	Path         string
	Scope        string
	TokenTTL     time.Duration
	Stderr       io.Writer
	AllowedTeams []string
}

func (p ExternalProvider) Resolve(ctx context.Context, rejectedToken string) (string, error) {
	credential, err := Load(p.Path, p.Scope)
	if err == nil && enforceCredential(Config{AllowedTeams: p.AllowedTeams}, credential) == nil && rejectedToken == "" && credentialFresh(credential, time.Now()) {
		return credential.Key, nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	lock, err := acquireFileLock(ctx, p.Path)
	if err != nil {
		return "", err
	}
	defer lock.release()

	credential, err = Load(p.Path, p.Scope)
	if err == nil {
		policyErr := enforceCredential(Config{AllowedTeams: p.AllowedTeams}, credential)
		if policyErr == nil && rejectedToken != "" && credential.Key != rejectedToken {
			return credential.Key, nil
		}
		if policyErr == nil && rejectedToken == "" && credentialFresh(credential, time.Now()) {
			return credential.Key, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}

	credential, err = p.run(ctx, rejectedToken != "" || err == nil)
	if err != nil {
		return "", err
	}
	credential.PrincipalType, credential.PrincipalID, credential.TeamID = jwtPrincipal(credential.Key)
	if credential.PrincipalType == "Team" && credential.TeamID == "" {
		credential.TeamID = credential.PrincipalID
	}
	if err := enforceCredential(Config{AllowedTeams: p.AllowedTeams}, credential); err != nil {
		return "", err
	}
	if err := saveCredential(p.Path, p.Scope, credential); err != nil {
		return "", err
	}
	return credential.Key, nil
}

func (p ExternalProvider) run(ctx context.Context, refresh bool) (Credential, error) {
	timeout := 60 * time.Second
	if refresh {
		timeout = 5 * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var stdout strings.Builder
	command := exec.CommandContext(runCtx, "sh", "-c", p.Command)
	command.Stdin = nil
	command.Stdout = &stdout
	command.Stderr = p.Stderr
	if refresh {
		command.Env = append(os.Environ(), "GROK_AUTH_EXPIRED=1")
	}
	if err := command.Run(); err != nil {
		if runCtx.Err() != nil {
			return Credential{}, fmt.Errorf("external auth provider timed out after %s", timeout)
		}
		return Credential{}, fmt.Errorf("external auth provider failed: %w", err)
	}
	return parseExternalCredential(stdout.String(), p.TokenTTL, time.Now())
}

func parseExternalCredential(output string, tokenTTL time.Duration, now time.Time) (Credential, error) {
	output = strings.TrimSpace(output)
	if output == "" {
		return Credential{}, errors.New("external auth provider produced no output")
	}
	var wire struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	token := output
	if json.Unmarshal([]byte(output), &wire) == nil {
		if strings.TrimSpace(wire.AccessToken) == "" {
			return Credential{}, errors.New("external auth provider JSON has no access_token")
		}
		token = wire.AccessToken
		if wire.ExpiresIn < 0 {
			return Credential{}, errors.New("external auth provider expires_in must not be negative")
		}
		if wire.ExpiresIn > 0 {
			tokenTTL = time.Duration(wire.ExpiresIn) * time.Second
		}
	}
	created := now.UTC()
	var expiresAt *time.Time
	if tokenTTL > 0 {
		value := created.Add(tokenTTL)
		expiresAt = &value
	}
	return Credential{
		Key: token, AuthMode: "external", CreateTime: created,
		CodingDataRetentionOptOut: true, RefreshToken: wire.RefreshToken, ExpiresAt: expiresAt,
	}, nil
}

func credentialFresh(credential Credential, now time.Time) bool {
	return credential.ExpiresAt == nil || credential.ExpiresAt.After(now.Add(5*time.Minute))
}
