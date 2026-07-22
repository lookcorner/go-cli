package acp

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/lookcorner/go-cli/internal/workspace"
)

func (s *Server) handleContentSearch(ctx context.Context, incoming message) {
	var req struct {
		SessionID        string   `json:"sessionId"`
		CWD              string   `json:"cwd"`
		Pattern          string   `json:"pattern"`
		CaseInsensitive  bool     `json:"caseInsensitive"`
		WholeWord        bool     `json:"wholeWord"`
		IsRegex          bool     `json:"isRegex"`
		IncludeGlobs     []string `json:"includeGlobs"`
		ExcludeGlobs     []string `json:"excludeGlobs"`
		MaxFiles         *int     `json:"maxFiles"`
		MaxMatches       *int     `json:"maxMatches"`
		RespectGitignore *bool    `json:"respectGitignore"`
	}
	if json.Unmarshal(incoming.Params, &req) != nil {
		s.respondError(incoming.ID, -32602, "invalid search parameters")
		return
	}
	if (req.MaxFiles != nil && *req.MaxFiles < 0) || (req.MaxMatches != nil && *req.MaxMatches < 0) {
		s.respondError(incoming.ID, -32602, "search limits must not be negative")
		return
	}
	root := strings.TrimSpace(req.CWD)
	if root == "" && req.SessionID != "" {
		if current := s.lookupSession(req.SessionID); current != nil {
			root = current.cwd
		} else {
			s.respondError(incoming.ID, -32602, "session not found: "+req.SessionID)
			return
		}
	}
	if root == "" {
		s.respondError(incoming.ID, -32602, "either cwd or sessionId is required")
		return
	}
	respectGitignore := true
	if req.RespectGitignore != nil {
		respectGitignore = *req.RespectGitignore
	}
	contextID := req.SessionID
	if contextID == "" {
		contextID = "agent"
	}
	result, err := workspace.SearchContent(ctx, root, workspace.ContentSearchRequest{
		Pattern: req.Pattern, CaseInsensitive: req.CaseInsensitive, WholeWord: req.WholeWord, IsRegex: req.IsRegex,
		IncludeGlobs: req.IncludeGlobs, ExcludeGlobs: req.ExcludeGlobs, MaxFiles: req.MaxFiles, MaxMatches: req.MaxMatches,
		RespectGitignore: respectGitignore,
	}, func(batch workspace.ContentSearchBatch) {
		s.write(map[string]any{
			"jsonrpc": "2.0", "method": "x.ai/search/content/status",
			"params": map[string]any{
				"sessionId": contextID, "files": batch.Files, "totalMatches": batch.TotalMatches,
				"totalFiles": batch.TotalFiles, "done": batch.Done, "truncated": batch.Truncated,
			},
		})
	})
	if err != nil {
		s.respondError(incoming.ID, -32000, err.Error())
		return
	}
	s.respond(incoming.ID, map[string]any{"result": result, "error": nil})
}
