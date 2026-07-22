package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/lookcorner/go-cli/internal/workspace"
)

const folderTrustPromptTimeout = 30 * time.Minute

type folderTrustResult struct {
	outcome string
	err     error
}

func parseClientFolderTrust(raw json.RawMessage) bool {
	var params struct {
		ClientCapabilities struct {
			Meta map[string]struct {
				Interactive bool `json:"interactive"`
			} `json:"_meta"`
		} `json:"clientCapabilities"`
	}
	if json.Unmarshal(raw, &params) != nil {
		return false
	}
	return params.ClientCapabilities.Meta["x.ai/folderTrust"].Interactive
}

func (s *Server) startFolderTrustPrompt(current *session) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.maybePromptFolderTrust(current)
	}()
}

func (s *Server) maybePromptFolderTrust(current *session) {
	if current == nil || !s.clientFolderTrust || !s.FolderTrustEnabled || workspace.ResolveFolderTrust(current.cwd, true, true) != workspace.TrustPrompt {
		return
	}
	key := workspace.WorkspaceTrustKey(current.cwd)
	s.trustMu.Lock()
	if s.trustPrompted[key] {
		s.trustMu.Unlock()
		return
	}
	if s.trustPrompted == nil {
		s.trustPrompted = make(map[string]bool)
	}
	s.trustPrompted[key] = true
	s.trustMu.Unlock()

	id := fmt.Sprintf("gork-folder-trust-%d", s.nextRequest.Add(1))
	result := make(chan folderTrustResult, 1)
	s.mu.Lock()
	if s.closing.Load() {
		s.mu.Unlock()
		s.releaseFolderTrustPrompt(key)
		return
	}
	if s.pendingTrust == nil {
		s.pendingTrust = make(map[string]chan folderTrustResult)
	}
	s.pendingTrust[id] = result
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.pendingTrust, id)
		s.mu.Unlock()
	}()

	s.write(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": "x.ai/folder_trust/request",
		"params": map[string]any{
			"sessionId": current.id, "cwd": current.cwd, "workspace": key,
			"configKinds": workspace.ProjectExecutionConfigKinds(current.cwd),
		},
	})
	timeout := s.trustPromptTimeout
	if timeout <= 0 {
		timeout = folderTrustPromptTimeout
	}
	parent := s.trustContext
	if parent == nil {
		parent = current.ctx
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	var response folderTrustResult
	select {
	case response = <-result:
	case <-ctx.Done():
		response.err = ctx.Err()
	}
	if response.err != nil {
		s.releaseFolderTrustPrompt(key)
		return
	}
	if response.outcome != "trust" {
		return
	}
	if !s.grantPromptedFolder(ctx, key, current.cwd) {
		return
	}
	s.reloadTrustedWorkspace(ctx, key)
}

func handleFolderTrustResponse(pending chan folderTrustResult, incoming message) {
	result := folderTrustResult{}
	if len(incoming.Error) > 0 && string(incoming.Error) != "null" {
		result.err = errors.New("ACP folder trust request failed")
	} else {
		var response struct {
			Outcome string `json:"outcome"`
		}
		if json.Unmarshal(incoming.Result, &response) != nil || response.Outcome == "" {
			result.err = errors.New("invalid ACP folder trust response")
		} else if response.Outcome == "trust" {
			result.outcome = "trust"
		} else {
			result.outcome = "reject"
		}
	}
	select {
	case pending <- result:
	default:
	}
}

func (s *Server) releaseFolderTrustPrompt(key string) {
	s.trustMu.Lock()
	delete(s.trustPrompted, key)
	s.trustMu.Unlock()
}

func (s *Server) grantPromptedFolder(ctx context.Context, key, cwd string) bool {
	s.trustMu.Lock()
	defer s.trustMu.Unlock()
	if !s.trustPrompted[key] {
		return false
	}
	if err := workspace.GrantFolderTrust(ctx, cwd); err != nil {
		delete(s.trustPrompted, key)
		return false
	}
	return true
}

func (s *Server) clearFolderTrustPrompt(cwd string) {
	s.releaseFolderTrustPrompt(workspace.WorkspaceTrustKey(cwd))
}

func (s *Server) reloadTrustedWorkspace(ctx context.Context, key string) {
	s.mu.Lock()
	all := make([]*session, 0, len(s.sessions))
	for _, current := range s.sessions {
		all = append(all, current)
	}
	s.mu.Unlock()
	var targets []*session
	for _, current := range all {
		if current != nil && workspace.WorkspaceTrustKey(current.cwd) == key {
			targets = append(targets, current)
		}
	}
	for _, current := range targets {
		if current.runner != nil && current.runner.UpdatePlugins != nil {
			_, _ = current.runner.UpdatePlugins(ctx, nil)
			return
		}
	}
	for _, current := range targets {
		if current.runner == nil {
			continue
		}
		if current.runner.ReloadMCPBase != nil {
			_ = current.runner.ReloadMCPBase(ctx)
		}
		if current.runner.ReloadHooks != nil {
			_ = current.runner.ReloadHooks()
		}
	}
}
