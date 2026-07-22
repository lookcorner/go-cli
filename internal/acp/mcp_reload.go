package acp

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
)

func (s *Server) handleMCPReload(ctx context.Context, incoming message) {
	target := ""
	if incoming.Method == "x.ai/internal/reload_project_mcp_servers" {
		var req struct {
			CWD string `json:"cwd"`
		}
		if json.Unmarshal(incoming.Params, &req) != nil || strings.TrimSpace(req.CWD) == "" || !filepath.IsAbs(req.CWD) {
			s.respondError(incoming.ID, -32602, "absolute cwd is required")
			return
		}
		target = filepath.Clean(req.CWD)
	}

	s.mu.Lock()
	sessions := make([]*session, 0, len(s.sessions))
	for _, current := range s.sessions {
		sessions = append(sessions, current)
	}
	s.mu.Unlock()

	reloaders := make([]func(context.Context) error, 0, len(sessions))
	for _, current := range sessions {
		if current == nil {
			continue
		}
		current.mu.Lock()
		closed, cwd, runner := current.closed, current.cwd, current.runner
		current.mu.Unlock()
		if closed || runner == nil || runner.ReloadMCPBase == nil || target != "" && !pathWithin(cwd, target) {
			continue
		}
		reloaders = append(reloaders, runner.ReloadMCPBase)
	}

	for _, reload := range reloaders {
		_ = reload(ctx)
	}
	s.respond(incoming.ID, map[string]any{"updated": len(reloaders)})
}

func pathWithin(path, root string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	return err == nil && rel != ".." && !filepath.IsAbs(rel) && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
