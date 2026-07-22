package acp

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/lookcorner/go-cli/internal/tools"
)

func (s *Server) RequestPlanModeExit(ctx context.Context, sessionID string, event tools.PlanModeEvent) (tools.PlanModeDecision, error) {
	s.beginRosterInteraction(sessionID)
	defer s.endRosterInteraction(sessionID)
	id := fmt.Sprintf("gork-plan-approval-%d", s.nextRequest.Add(1))
	result := make(chan planApprovalResult, 1)
	s.mu.Lock()
	s.pendingPlan[id] = result
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.pendingPlan, id)
		s.mu.Unlock()
	}()
	var content any
	if strings.TrimSpace(event.PlanContent) != "" {
		content = event.PlanContent
	}
	s.write(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": "x.ai/exit_plan_mode",
		"params": map[string]any{
			"sessionId": sessionID, "toolCallId": event.ToolCallID, "planContent": content,
		},
	})
	select {
	case response := <-result:
		return response.decision, response.err
	case <-ctx.Done():
		return tools.PlanModeDecision{}, ctx.Err()
	}
}

func (s *Server) NotifyPlanModeChanged(sessionID, mode string) {
	current := s.lookupSession(sessionID)
	if current == nil {
		return
	}
	current.mu.Lock()
	current.mode = mode
	current.updated = time.Now().UTC()
	current.mu.Unlock()
	s.notify(sessionID, map[string]any{"sessionUpdate": "current_mode_update", "currentModeId": mode})
}
