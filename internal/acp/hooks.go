package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/hooks"
	"github.com/lookcorner/go-cli/internal/workspace"
)

func (s *Server) handleHooks(ctx context.Context, incoming message) {
	var req struct {
		SessionID string `json:"sessionId"`
		Action    struct {
			Type       string   `json:"type"`
			Path       string   `json:"path"`
			HookName   string   `json:"hook_name"`
			HookName2  string   `json:"hookName"`
			HookNames  []string `json:"hook_names"`
			HookNames2 []string `json:"hookNames"`
			Disable    bool     `json:"disable"`
		} `json:"action"`
	}
	if json.Unmarshal(incoming.Params, &req) != nil || req.SessionID == "" {
		s.respondError(incoming.ID, -32602, "sessionId is required")
		return
	}
	current := s.lookupSession(req.SessionID)
	if current == nil || current.runner == nil {
		s.respondError(incoming.ID, -32602, "session not found")
		return
	}
	if incoming.Method == "x.ai/hooks/action" {
		names := req.Action.HookNames
		if len(names) == 0 {
			names = req.Action.HookNames2
		}
		s.handleHookAction(ctx, incoming, current, req.Action.Type, req.Action.Path, firstString(req.Action.HookName, req.Action.HookName2), names, req.Action.Disable)
		return
	}
	snapshot := hooks.Snapshot{}
	if current.runner.HookCatalog != nil {
		snapshot = current.runner.HookCatalog.Snapshot()
	}
	items := make([]map[string]any, 0, len(snapshot.Hooks))
	for _, item := range snapshot.Hooks {
		wire := map[string]any{
			"name": item.Name, "event": item.Event, "handlerType": item.Type,
			"timeoutMs": item.Timeout.Milliseconds(), "sourceDir": item.SourceDir, "disabled": item.Disabled,
		}
		if item.Matcher != "" {
			wire["matcher"] = item.Matcher
		}
		if item.Command != "" {
			wire["command"] = item.Command
		}
		if item.URL != "" {
			wire["url"] = item.URL
		}
		items = append(items, wire)
	}
	s.respond(incoming.ID, map[string]any{"result": map[string]any{
		"hooks": items, "projectTrusted": current.runner.HookCatalog != nil && current.runner.HookCatalog.ProjectTrusted(), "loadErrors": snapshot.LoadErrors,
	}, "error": nil})
}

func (s *Server) handleHookAction(ctx context.Context, incoming message, current *session, action, path, hookName string, hookNames []string, disable bool) {
	switch action {
	case "reload":
		if current.runner.ReloadHooks == nil {
			s.hookActionOutcome(incoming, "unsupported", "Hook configuration is read-only.", false, false)
			return
		}
		if err := current.runner.ReloadHooks(); err != nil {
			s.hookActionOutcome(incoming, "error", err.Error(), false, false)
			return
		}
		s.hookActionOutcome(incoming, "success", "Hooks reloaded.", true, false)
	case "enable", "disable":
		if current.runner.HookCatalog == nil || hookName == "" {
			s.hookActionOutcome(incoming, "validation_error", "hookName is required.", false, false)
			return
		}
		if err := current.runner.HookCatalog.SetDisabled(ctx, []string{hookName}, action == "disable"); err != nil {
			s.hookActionOutcome(incoming, "error", err.Error(), false, false)
			return
		}
		s.hookActionOutcome(incoming, "success", "Hook state updated.", true, false)
	case "toggle_source":
		if current.runner.HookCatalog == nil || len(hookNames) == 0 {
			s.hookActionOutcome(incoming, "validation_error", "hookNames are required.", false, false)
			return
		}
		if err := current.runner.HookCatalog.SetDisabled(ctx, hookNames, disable); err != nil {
			s.hookActionOutcome(incoming, "error", err.Error(), false, false)
			return
		}
		s.hookActionOutcome(incoming, "success", "Hook source state updated.", true, false)
	case "trust":
		root, ok := workspace.FindGitRoot(current.cwd)
		if !ok {
			s.hookActionOutcome(incoming, "validation_error", "Project hooks require a Git worktree.", false, false)
			return
		}
		if err := workspace.GrantFolderTrust(ctx, root); err != nil {
			s.hookActionOutcome(incoming, "error", err.Error(), false, false)
			return
		}
		if current.runner.UpdatePlugins != nil {
			if _, err := current.runner.UpdatePlugins(ctx, nil); err != nil {
				s.hookActionOutcome(incoming, "error", err.Error(), false, false)
				return
			}
		} else if current.runner.ReloadHooks != nil {
			_ = current.runner.ReloadHooks()
		}
		s.hookActionOutcome(incoming, "success", "Workspace trusted and executable components reloaded.", false, false)
	case "untrust":
		root, ok := workspace.FindGitRoot(current.cwd)
		if !ok {
			s.hookActionOutcome(incoming, "validation_error", "Project hooks require a Git worktree.", false, false)
			return
		}
		s.clearFolderTrustPrompt(root)
		if err := workspace.RevokeFolderTrust(ctx, root); err != nil {
			s.hookActionOutcome(incoming, "error", err.Error(), false, false)
			return
		}
		if current.runner.UpdatePlugins != nil {
			if _, err := current.runner.UpdatePlugins(ctx, nil); err != nil {
				s.hookActionOutcome(incoming, "error", err.Error(), false, false)
				return
			}
		} else if current.runner.ReloadHooks != nil {
			_ = current.runner.ReloadHooks()
		}
		s.hookActionOutcome(incoming, "success", "Workspace untrusted and project components unloaded.", false, false)
	case "add", "remove":
		var err error
		if action == "add" {
			err = hooks.AddPath(ctx, path)
		} else {
			err = hooks.RemovePath(ctx, path)
		}
		if err != nil {
			s.hookActionOutcome(incoming, "validation_error", err.Error(), false, false)
			return
		}
		if current.runner.ReloadHooks != nil {
			if err := current.runner.ReloadHooks(); err != nil {
				s.hookActionOutcome(incoming, "error", err.Error(), false, false)
				return
			}
		}
		s.hookActionOutcome(incoming, "success", "Hook path updated.", false, false)
	default:
		s.hookActionOutcome(incoming, "validation_error", "Unknown hook action.", false, false)
	}
}

func (s *Server) hookActionOutcome(incoming message, status, message string, reload, restart bool) {
	s.respond(incoming.ID, map[string]any{"result": map[string]any{
		"status": status, "message": message, "requiresReload": reload, "requiresRestart": restart,
	}, "error": nil})
}

func (s *Server) handleHookSlashPrompt(ctx context.Context, incoming message, current *session, lifecycle promptLifecycle, action, path string) {
	var text string
	switch action {
	case "list":
		text = hookListText(current.runner.HookCatalog)
	case "trust":
		root, ok := workspace.FindGitRoot(current.cwd)
		if !ok {
			text = "Project hooks require a Git worktree."
		} else if err := workspace.GrantFolderTrust(ctx, root); err != nil {
			text = err.Error()
		} else {
			text = fmt.Sprintf("Trusted: %s.", root)
		}
	case "untrust":
		root, ok := workspace.FindGitRoot(current.cwd)
		if !ok {
			text = "Project hooks require a Git worktree."
		} else if current.runner.HookCatalog == nil || !current.runner.HookCatalog.ProjectTrusted() {
			text = fmt.Sprintf("Not currently trusted: %s", root)
		} else {
			s.clearFolderTrustPrompt(root)
			if err := workspace.RevokeFolderTrust(ctx, root); err != nil {
				text = err.Error()
			} else {
				text = fmt.Sprintf("Untrusted: %s.", root)
			}
		}
	case "add", "remove":
		if path == "" {
			if action == "add" {
				text = "Usage: /hooks add <path>\nProvide a path to a hook JSON file or directory under ~/.grok/."
			} else {
				text = "Usage: /hooks-remove <path>\nProvide the path to remove from hooks-paths."
			}
			break
		}
		var err error
		if action == "add" {
			err = hooks.AddPath(ctx, path)
		} else {
			err = hooks.RemovePath(ctx, path)
		}
		if err != nil {
			text = fmt.Sprintf("Failed to %s hook path: %v", action, err)
		} else if action == "add" {
			text = fmt.Sprintf("Added hook path: %s\nRestart session to load hooks from this path.", path)
		} else {
			text = fmt.Sprintf("Removed hook path: %s\nRestart session to stop loading hooks from this path.", path)
		}
	}
	s.sendCommandOutput(current.id, text)
	s.finishPrompt(incoming, current, lifecycle, "end_turn", agent.Result{}, nil, "")
}

func hookListText(catalog *hooks.Catalog) string {
	if catalog == nil {
		return "No hooks loaded for this session."
	}
	items := catalog.Snapshot().Hooks
	if len(items) == 0 {
		return "No hooks loaded for this session."
	}
	lines := []string{fmt.Sprintf("Loaded hooks (%d):", len(items))}
	for _, item := range items {
		matcher := ""
		if item.Matcher != "" {
			matcher = "  matcher: " + item.Matcher
		}
		target := "target: <none>"
		if item.Command != "" {
			target = "command: " + item.Command
		} else if item.URL != "" {
			target = "url: " + item.URL
		}
		lines = append(lines, fmt.Sprintf("  %s%s  %s  timeout: %ds", item.Name, matcher, target, int64(item.Timeout.Seconds())))
	}
	return strings.Join(lines, "\n")
}
