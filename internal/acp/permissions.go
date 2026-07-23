package acp

import (
	"encoding/json"

	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/tools"
)

func (s *Server) handleAlwaysApprovePrompt(incoming message, current *session, lifecycle promptLifecycle, enabled bool) {
	current.mu.Lock()
	if current.closed {
		current.mu.Unlock()
		s.failPrompt(incoming, current, lifecycle, "session is closed")
		return
	}
	if current.running {
		current.mu.Unlock()
		s.failPrompt(incoming, current, lifecycle, "session already has an active prompt")
		return
	}
	runner := current.runner
	current.mu.Unlock()

	if runner == nil || runner.Tools == nil {
		s.failPrompt(incoming, current, lifecycle, "permission mode cannot be changed")
		return
	}
	setAlwaysApprove(current, enabled)
	s.finishPrompt(incoming, current, lifecycle, "end_turn", agent.Result{}, nil, "")
}

func setAlwaysApprove(current *session, enabled bool) {
	current.mu.Lock()
	defer current.mu.Unlock()
	if current.runner == nil || current.runner.Tools == nil {
		return
	}
	mode, ok := current.runner.Tools.PermissionMode()
	if !ok {
		return
	}
	if enabled {
		if mode != tools.PermissionAlwaysApprove && current.runner.Tools.SetPermissionMode(tools.PermissionAlwaysApprove) == nil {
			current.modeBeforeYolo = mode
		}
		return
	}
	if mode != tools.PermissionAlwaysApprove {
		return
	}
	previous := current.modeBeforeYolo
	if previous != tools.PermissionPrompt && previous != tools.PermissionAuto {
		previous = tools.PermissionPrompt
	}
	if current.runner.Tools.SetPermissionMode(previous) == nil {
		current.modeBeforeYolo = ""
	}
}

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
			setAlwaysApprove(current, *yoloMode)
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

func (s *Server) handlePermissionReset() {
	s.mu.Lock()
	sessions := make([]*session, 0, len(s.sessions))
	for _, current := range s.sessions {
		sessions = append(sessions, current)
	}
	s.mu.Unlock()
	approvers := make([]*serverApprover, 0, len(sessions))
	for _, current := range sessions {
		current.mu.Lock()
		approver, closed := current.permissions, current.closed
		current.mu.Unlock()
		if !closed && approver != nil {
			approvers = append(approvers, approver)
		}
	}
	for _, approver := range approvers {
		approver.reset()
	}
}
