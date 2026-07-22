package acp

import (
	"encoding/json"

	"github.com/lookcorner/go-cli/internal/agent"
)

func (s *Server) handleEvictSessions(raw json.RawMessage) {
	var params struct {
		SessionIDs []string `json:"sessionIds"`
	}
	if json.Unmarshal(raw, &params) != nil || len(params.SessionIDs) == 0 {
		return
	}
	for _, id := range params.SessionIDs {
		s.closeIdleSession(id)
	}
}

func (s *Server) closeIdleSession(id string) bool {
	s.mu.Lock()
	current := s.sessions[id]
	if current == nil {
		s.mu.Unlock()
		return false
	}
	current.mu.Lock()
	live := sessionStateHasLiveWork(current) || taskSnapshotHasLiveWork(current.runner.TaskSnapshot()) ||
		s.terminals != nil && s.terminals.hasLiveSession(id)
	if live {
		current.mu.Unlock()
		s.mu.Unlock()
		return false
	}
	current.closed = true
	delete(s.sessions, id)
	current.mu.Unlock()
	s.mu.Unlock()
	s.shutdownSession(current)
	return true
}

func sessionStateHasLiveWork(current *session) bool {
	return current.running || current.runDone != nil || current.btwDone != nil || current.recapDone != nil || current.suggestDone != nil ||
		len(current.wakeQueue) > 0 || len(current.interjectionQueue) > 0 || len(current.promptQueue) > 0 ||
		current.runningPromptID != "" || current.startingPromptID != ""
}

func taskSnapshotHasLiveWork(snapshot agent.TaskSnapshot) bool {
	for _, subagent := range snapshot.Subagents {
		if subagent.Status == "running" {
			return true
		}
	}
	for _, process := range snapshot.Processes {
		if !process.Completed {
			return true
		}
	}
	return len(snapshot.Scheduled) > 0
}
