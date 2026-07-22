package acp

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"

	sessionlog "github.com/lookcorner/go-cli/internal/session"
)

func (s *Server) handleSessionRepair(incoming message) {
	var req struct {
		SessionID string `json:"sessionId"`
		DryRun    bool   `json:"dryRun"`
	}
	if json.Unmarshal(incoming.Params, &req) != nil || strings.TrimSpace(req.SessionID) == "" {
		s.respondError(incoming.ID, -32602, "sessionId is required")
		return
	}
	if current := s.lookupSession(req.SessionID); current != nil {
		current.mu.Lock()
		defer current.mu.Unlock()
		if current.closed || current.runner == nil {
			s.respondError(incoming.ID, -32004, "session not found: "+req.SessionID)
			return
		}
		if current.running || current.startingPromptID != "" || current.btwDone != nil || current.recapDone != nil || current.suggestDone != nil {
			s.respondError(incoming.ID, -32000, "cannot repair history while session activity is running")
			return
		}
		repairer, ok := current.runner.Logger.(interface {
			RepairHistory(bool) (sessionlog.HistoryRepairReport, error)
		})
		if !ok {
			s.respondError(incoming.ID, -32000, "session history repair is unavailable")
			return
		}
		var transcript []sessionlog.Message
		if !req.DryRun {
			var err error
			transcript, err = sessionlog.TranscriptOrEmpty(current.logPath)
			if err != nil {
				s.respondError(incoming.ID, -32000, err.Error())
				return
			}
		}
		report, err := repairer.RepairHistory(req.DryRun)
		if err != nil {
			s.respondError(incoming.ID, -32000, err.Error())
			return
		}
		if report.Changed() && !req.DryRun {
			current.runner.RestoreHistory(transcript)
			current.previous = ""
			current.updated = time.Now().UTC()
		}
		s.respond(incoming.ID, repairSessionResponse(report, req.DryRun, true))
		return
	}

	path, err := sessionlog.PathForID(s.SessionDir, req.SessionID)
	if err != nil {
		s.respondError(incoming.ID, -32602, err.Error())
		return
	}
	report, err := sessionlog.RepairHistory(path, req.DryRun)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			s.respondError(incoming.ID, -32004, "session not found: "+req.SessionID)
		} else {
			s.respondError(incoming.ID, -32000, err.Error())
		}
		return
	}
	s.respond(incoming.ID, repairSessionResponse(report, req.DryRun, false))
}

func repairSessionResponse(report sessionlog.HistoryRepairReport, dryRun, resident bool) map[string]any {
	stripped := report.StrippedToolResultIDs
	if stripped == nil {
		stripped = []string{}
	}
	return map[string]any{
		"repaired": report.Changed(), "dryRun": dryRun, "resident": resident,
		"duplicatesRemoved": report.DuplicatesRemoved, "strippedToolResultIds": stripped,
		"syntheticResultsInserted": report.SyntheticResultsInserted,
	}
}
