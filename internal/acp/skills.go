package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lookcorner/go-cli/internal/skills"
)

func (s *Server) handleSkills(ctx context.Context, incoming message) {
	if incoming.Method == "x.ai/skills/refresh-baseline" || incoming.Method == "x.ai/internal/reload_skills" {
		s.handleSkillRefresh(incoming)
		return
	}
	var req struct {
		CWD     string `json:"cwd"`
		Path    string `json:"path"`
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
	}
	if json.Unmarshal(incoming.Params, &req) != nil {
		s.respondError(incoming.ID, -32602, "invalid skills parameters")
		return
	}
	var current *session
	s.mu.Lock()
	for _, candidate := range s.sessions {
		if req.CWD == "" || candidate.cwd == req.CWD {
			current = candidate
			break
		}
	}
	s.mu.Unlock()
	if current == nil || current.runner == nil || current.runner.Skills == nil {
		s.respond(incoming.ID, map[string]any{"result": nil, "error": "skills catalog is unavailable for this cwd"})
		return
	}
	switch incoming.Method {
	case "x.ai/skills/list":
		s.respond(incoming.ID, map[string]any{"result": map[string]any{"skills": current.runner.Skills.List()}})
	case "x.ai/skills/config":
		s.respond(incoming.ID, map[string]any{"result": current.runner.Skills.ConfigInfo()})
	default:
		s.handleSkillsMutation(ctx, incoming, current, req.CWD, req.Path, req.Name, req.Enabled)
	}
}

func (s *Server) handleSkillRefresh(incoming message) {
	s.mu.Lock()
	sessions := make([]*session, 0, len(s.sessions))
	for _, current := range s.sessions {
		sessions = append(sessions, current)
	}
	s.mu.Unlock()
	for _, current := range sessions {
		if current != nil && current.runner != nil && current.runner.Skills != nil {
			_ = current.runner.Skills.Refresh()
		}
	}
	if incoming.Method == "x.ai/internal/reload_skills" {
		s.respond(incoming.ID, map[string]any{"reloaded": len(sessions)})
		return
	}
	s.respond(incoming.ID, map[string]any{"ok": true})
}

func (s *Server) handleSkillsMutation(ctx context.Context, incoming message, current *session, cwd, path, name string, enabled bool) {
	if current.runner.UpdateSkills == nil {
		s.respond(incoming.ID, map[string]any{"result": nil, "error": "skills configuration is read-only"})
		return
	}
	if cwd == "" {
		cwd = current.cwd
	}
	resolved := ""
	if incoming.Method == "x.ai/skills/add" || incoming.Method == "x.ai/skills/remove" {
		if strings.TrimSpace(path) == "" {
			s.respondError(incoming.ID, -32602, "path is required")
			return
		}
		resolved = skills.ResolveConfigPath(path, cwd)
	}
	if incoming.Method == "x.ai/skills/toggle" {
		if strings.TrimSpace(name) == "" {
			s.respondError(incoming.ID, -32602, "name is required")
			return
		}
		found := false
		for _, item := range current.runner.Skills.List() {
			if item.Name == name {
				found = true
				break
			}
		}
		if !found {
			s.respond(incoming.ID, map[string]any{"result": nil, "error": fmt.Sprintf("Skill %q not found", name)})
			return
		}
	}
	_, err := current.runner.UpdateSkills(ctx, func(settings *skills.Settings) {
		switch incoming.Method {
		case "x.ai/skills/add":
			settings.Ignore = removeOverlappingPaths(settings.Ignore, resolved)
			if !containsString(settings.Paths, resolved) {
				settings.Paths = append(settings.Paths, resolved)
			}
		case "x.ai/skills/remove":
			settings.Paths = removeString(settings.Paths, resolved)
		case "x.ai/skills/reset":
			*settings = skills.Settings{}
		case "x.ai/skills/toggle":
			if enabled {
				settings.Disabled = removeString(settings.Disabled, name)
			} else if !containsString(settings.Disabled, name) {
				settings.Disabled = append(settings.Disabled, name)
			}
		}
	})
	if err != nil {
		s.respond(incoming.ID, map[string]any{"result": nil, "error": err.Error()})
		return
	}
	info := current.runner.Skills.ConfigInfo()
	switch incoming.Method {
	case "x.ai/skills/add":
		added := 0
		for _, item := range info.Skills {
			if strings.HasPrefix(item.Path, resolved) {
				added++
			}
		}
		s.respond(incoming.ID, map[string]any{"result": map[string]any{
			"addedCount": added, "total": info.TotalSkills, "path": resolved, "skills": info.Skills,
			"message": fmt.Sprintf("Added path %s. %d new skill%s found (%d total).", resolved, added, pluralS(added), info.TotalSkills),
		}})
	case "x.ai/skills/remove":
		s.respond(incoming.ID, map[string]any{"result": map[string]any{
			"path": resolved, "skills": info.Skills,
			"message": fmt.Sprintf("Removed path %s. %d skill%s remaining.", resolved, info.TotalSkills, pluralS(info.TotalSkills)),
		}})
	case "x.ai/skills/reset":
		s.respond(incoming.ID, map[string]any{"result": map[string]any{"skills": info.Skills, "message": "Custom skills config reset"}})
	case "x.ai/skills/toggle":
		s.respond(incoming.ID, map[string]any{"result": map[string]any{"skills": info.Skills}})
	}
}

func pluralS(count int) string {
	if count == 1 {
		return ""
	}
	return "s"
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func removeString(values []string, target string) []string {
	filtered := values[:0]
	for _, value := range values {
		if value != target {
			filtered = append(filtered, value)
		}
	}
	return filtered
}

func removeOverlappingPaths(values []string, target string) []string {
	filtered := values[:0]
	for _, value := range values {
		if value != target && !strings.HasPrefix(target, value) && !strings.HasPrefix(value, target) {
			filtered = append(filtered, value)
		}
	}
	return filtered
}
