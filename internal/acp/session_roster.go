package acp

import (
	"context"
	"sort"
	"time"

	sessionlog "github.com/lookcorner/go-cli/internal/session"
	"github.com/lookcorner/go-cli/internal/worktree"
)

type rosterEntry struct {
	SessionID        string         `json:"sessionId"`
	Title            *string        `json:"title"`
	CWD              string         `json:"cwd"`
	IsWorktree       bool           `json:"isWorktree"`
	ModelID          *string        `json:"modelId"`
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
		id, title, cwd, model, activity string
		changed                         time.Time
	}
	var live []liveSnapshot
	s.mu.Lock()
	for _, current := range s.sessions {
		current.mu.Lock()
		activity := "idle"
		if current.running {
			activity = "working"
		}
		title, model, changed := current.title, "", current.updated
		if current.runner != nil {
			model = current.runner.Model
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
		live = append(live, liveSnapshot{id: current.id, title: title, cwd: current.cwd, model: model, activity: activity, changed: changed})
		current.mu.Unlock()
	}
	s.mu.Unlock()
	for _, current := range live {
		rows = append(rows, newRosterEntry(ctx, current.id, current.title, current.cwd, current.model, current.activity, true, current.changed))
	}
	for _, summary := range byID {
		rows = append(rows, newRosterEntry(ctx, summary.Info.ID, summary.SessionSummary, summary.Info.CWD, summary.CurrentModelID, "dormant", false, summary.UpdatedAt))
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].LastChangeUnixMS == rows[j].LastChangeUnixMS {
			return rows[i].SessionID < rows[j].SessionID
		}
		return rows[i].LastChangeUnixMS > rows[j].LastChangeUnixMS
	})
	s.respond(incoming.ID, map[string]any{"result": map[string]any{"sessions": rows}, "error": nil})
}

func newRosterEntry(ctx context.Context, id, title, cwd, model, activity string, resident bool, changed time.Time) rosterEntry {
	return rosterEntry{
		SessionID: id, Title: optionalRosterString(title), CWD: cwd,
		IsWorktree: isRosterWorktree(ctx, cwd), ModelID: optionalRosterString(model),
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
