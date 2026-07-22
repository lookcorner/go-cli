package acp

import (
	"encoding/json"

	"github.com/lookcorner/go-cli/internal/tools"
)

func (s *Server) handleYoloModeChanged(raw json.RawMessage) {
	var params struct {
		YoloMode *bool `json:"yolo_mode"`
	}
	if json.Unmarshal(raw, &params) != nil || params.YoloMode == nil {
		return
	}
	mode := tools.PermissionPrompt
	if *params.YoloMode {
		mode = tools.PermissionAlwaysApprove
	}
	s.mu.Lock()
	sessions := make([]*session, 0, len(s.sessions))
	for _, current := range s.sessions {
		sessions = append(sessions, current)
	}
	s.mu.Unlock()
	for _, current := range sessions {
		current.mu.Lock()
		runner, closed := current.runner, current.closed
		current.mu.Unlock()
		if !closed && runner != nil && runner.Tools != nil {
			_ = runner.Tools.SetPermissionMode(mode)
		}
	}
}
