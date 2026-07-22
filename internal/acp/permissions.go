package acp

import (
	"encoding/json"

	"github.com/lookcorner/go-cli/internal/tools"
)

func (s *Server) handleYoloModeChanged(raw json.RawMessage) {
	var params struct {
		YoloMode       json.RawMessage `json:"yolo_mode"`
		AutoMode       json.RawMessage `json:"auto_mode"`
		PermissionMode json.RawMessage `json:"permission_mode"`
	}
	if json.Unmarshal(raw, &params) != nil {
		return
	}
	var yoloMode, autoMode *bool
	var yoloValue, autoValue bool
	var permissionMode string
	if json.Unmarshal(params.YoloMode, &yoloValue) == nil {
		yoloMode = &yoloValue
	}
	if json.Unmarshal(params.AutoMode, &autoValue) == nil {
		autoMode = &autoValue
	}
	_ = json.Unmarshal(params.PermissionMode, &permissionMode)
	if autoMode == nil {
		switch permissionMode {
		case "auto":
			value := true
			autoMode = &value
		case "always-approve", "ask", "default":
			value := false
			autoMode = &value
		}
	}
	if yoloMode == nil && autoMode == nil {
		return
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
		if closed || runner == nil || runner.Tools == nil {
			continue
		}
		if yoloMode != nil {
			mode := tools.PermissionPrompt
			if *yoloMode {
				mode = tools.PermissionAlwaysApprove
			}
			_ = runner.Tools.SetPermissionMode(mode)
			if *yoloMode {
				continue
			}
		}
		if autoMode != nil {
			if *autoMode {
				_ = runner.Tools.SetPermissionMode(tools.PermissionAuto)
			} else if mode, ok := runner.Tools.PermissionMode(); ok && mode == tools.PermissionAuto {
				_ = runner.Tools.SetPermissionMode(tools.PermissionPrompt)
			}
		}
	}
}
