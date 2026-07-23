package acp

import (
	"context"
	"sort"
	"time"

	sessionlog "github.com/lookcorner/go-cli/internal/session"
	"github.com/lookcorner/go-cli/internal/tools"
	"github.com/lookcorner/go-cli/internal/worktree"
)

type rosterEntry struct {
	SessionID        string         `json:"sessionId"`
	Title            *string        `json:"title"`
	CWD              string         `json:"cwd"`
	IsWorktree       bool           `json:"isWorktree"`
	ModelID          *string        `json:"modelId"`
	ReasoningEffort  *string        `json:"reasoningEffort,omitempty"`
	Yolo             bool           `json:"yolo"`
	Activity         string         `json:"activity"`
	Resident         bool           `json:"resident"`
	LastChangeUnixMS int64          `json:"lastChangeUnixMs"`
	Origin           map[string]any `json:"origin"`
}

func (s *Server) handleSessionRoster(ctx context.Context, incoming message) {
	summaries, err := sessionlog.Summaries(s.SessionDir, "", 0)
	if err != nil {
		s.respondError(incoming.ID, -32000, err.Error())
		return
	}
	byID := make(map[string]sessionlog.Summary, len(summaries))
	for _, summary := range summaries {
		byID[summary.Info.ID] = summary
	}
	rows := make([]rosterEntry, 0, len(summaries))
	type liveSnapshot struct {
		id, title, cwd, model, effort, activity string
		yolo                                    bool
		changed                                 time.Time
	}
	var live []liveSnapshot
	s.mu.Lock()
	for _, current := range s.sessions {
		current.mu.Lock()
		activity := "idle"
		if current.pendingInteractions > 0 {
			activity = "needs_input"
		} else if current.running {
			activity = "working"
		}
		title, cwd, model, effort, yolo, changed := current.title, current.cwd, "", "", false, current.updated
		if current.displayCWD != "" {
			cwd = current.displayCWD
		}
		if current.runner != nil {
			model = current.runner.Model
			effort = current.runner.ReasoningEffort
			if current.runner.Tools != nil {
				mode, ok := current.runner.Tools.PermissionMode()
				yolo = ok && mode == tools.PermissionAlwaysApprove
			}
		}
		if summary, ok := byID[current.id]; ok {
			if title == "" {
				title = summary.SessionSummary
			}
			if model == "" {
				model = summary.CurrentModelID
			}
			if changed.IsZero() {
				changed = summary.UpdatedAt
			}
			delete(byID, current.id)
		}
		live = append(live, liveSnapshot{id: current.id, title: title, cwd: cwd, model: model, effort: effort, yolo: yolo, activity: activity, changed: changed})
		current.mu.Unlock()
	}
	s.mu.Unlock()
	for _, current := range live {
		rows = append(rows, newRosterEntry(ctx, current.id, current.title, current.cwd, current.model, current.effort, current.activity, current.yolo, true, current.changed))
	}
	for _, summary := range byID {
		cwd := summary.Info.DisplayCWD
		if cwd == "" {
			cwd = summary.Info.CWD
		}
		rows = append(rows, newRosterEntry(ctx, summary.Info.ID, summary.SessionSummary, cwd, summary.CurrentModelID, "", "dormant", false, false, summary.UpdatedAt))
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].LastChangeUnixMS == rows[j].LastChangeUnixMS {
			return rows[i].SessionID < rows[j].SessionID
		}
		return rows[i].LastChangeUnixMS > rows[j].LastChangeUnixMS
	})
	s.respond(incoming.ID, map[string]any{"result": map[string]any{"sessions": rows}, "error": nil})
}

func (s *Server) notifyRosterUpsert(current *session, activity string) {
	if s.output == nil || current == nil {
		return
	}
	current.mu.Lock()
	if current.closed {
		current.mu.Unlock()
		return
	}
	if activity == "" {
		activity = "idle"
		if current.pendingInteractions > 0 {
			activity = "needs_input"
		} else if current.running {
			activity = "working"
		}
	}
	title, cwd, model, effort, yolo, changed := current.title, current.cwd, "", "", false, current.updated
	if current.displayCWD != "" {
		cwd = current.displayCWD
	}
	if current.runner != nil {
		model = current.runner.Model
		effort = current.runner.ReasoningEffort
		if current.runner.Tools != nil {
			mode, ok := current.runner.Tools.PermissionMode()
			yolo = ok && mode == tools.PermissionAlwaysApprove
		}
	}
	current.mu.Unlock()
	entry := newRosterEntry(context.Background(), current.id, title, cwd, model, effort, activity, yolo, true, changed)
	s.write(map[string]any{
		"jsonrpc": "2.0", "method": "x.ai/sessions/changed",
		"params": map[string]any{"upserted": []rosterEntry{entry}, "removed": []string{}},
	})
}

func (s *Server) notifyRosterRemoved(sessionID string) {
	if s.output == nil {
		return
	}
	s.write(map[string]any{
		"jsonrpc": "2.0", "method": "x.ai/sessions/changed",
		"params": map[string]any{"upserted": []rosterEntry{}, "removed": []string{sessionID}},
	})
}

func (s *Server) beginRosterInteraction(sessionID string) {
	current := s.lookupSession(sessionID)
	if current == nil {
		return
	}
	current.mu.Lock()
	current.pendingInteractions++
	current.updated = time.Now().UTC()
	current.mu.Unlock()
	s.notifyRosterUpsert(current, "needs_input")
}

func (s *Server) endRosterInteraction(sessionID string) {
	current := s.lookupSession(sessionID)
	if current == nil {
		return
	}
	current.mu.Lock()
	if current.pendingInteractions > 0 {
		current.pendingInteractions--
	}
	current.updated = time.Now().UTC()
	current.mu.Unlock()
	s.notifyRosterUpsert(current, "")
}

func newRosterEntry(ctx context.Context, id, title, cwd, model, effort, activity string, yolo, resident bool, changed time.Time) rosterEntry {
	return rosterEntry{
		SessionID: id, Title: optionalRosterString(title), CWD: cwd,
		IsWorktree: isRosterWorktree(ctx, cwd), ModelID: optionalRosterString(model),
		ReasoningEffort: optionalRosterString(effort), Yolo: yolo,
		Activity: activity, Resident: resident, LastChangeUnixMS: changed.UnixMilli(),
		Origin: map[string]any{"kind": "local"},
	}
}

func optionalRosterString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func isRosterWorktree(ctx context.Context, cwd string) bool {
	root, err := worktree.GitRoot(ctx, cwd)
	if err != nil {
		return false
	}
	main, err := worktree.MainRoot(ctx, cwd)
	return err == nil && root != main
}
