package tools

import (
	"strings"
	"time"
)

type GoalEvent struct {
	Kind string
	Data map[string]any
}

type GoalObserver interface {
	GoalEvent(GoalEvent)
}

func (r *Registry) SetGoalObserver(observer GoalObserver) {
	if r == nil || r.goal == nil {
		return
	}
	r.goal.mu.Lock()
	r.goal.observer = observer
	r.goal.mu.Unlock()
}

func (r *Registry) emitGoalEvent(kind string, data map[string]any) {
	if r == nil || r.goal == nil {
		return
	}
	r.goal.mu.Lock()
	observer := r.goal.observer
	r.goal.mu.Unlock()
	if observer != nil {
		observer.GoalEvent(GoalEvent{Kind: kind, Data: data})
	}
}

func (r *Registry) emitGoalUpdated(lastEvent string) {
	r.emitGoalUpdatedWith(lastEvent, nil)
}

func (r *Registry) emitGoalUpdatedWith(lastEvent string, fields map[string]any) {
	if r == nil || r.goal == nil {
		return
	}
	r.goal.mu.Lock()
	observer, event := r.goal.goalUpdatedLocked(lastEvent)
	for key, value := range fields {
		event.Data[key] = value
	}
	r.goal.mu.Unlock()
	if observer != nil {
		observer.GoalEvent(event)
	}
}

func (s *GoalStore) goalUpdatedLocked(lastEvent string) (GoalObserver, GoalEvent) {
	data := map[string]any{
		"objective": s.objective, "status": goalWireStatus(s.status, s.message, lastEvent), "phase": goalPhase(s.status),
		"classifier_max_runs": s.classifierMaxRuns, "last_event": lastEvent,
		"last_event_timestamp": time.Now().UTC().Format(time.RFC3339Nano),
		"elapsed_ms":           max(int64(0), time.Now().UnixMilli()-s.createdAtUnix*1000),
	}
	if s.verificationRuns > 0 {
		data["classifier_runs_attempted"] = s.verificationRuns
	}
	if s.status == "verifying" {
		data["verifying_completion"] = true
	}
	if s.message != "" {
		data["message"] = s.message
	}
	if s.plannerPlanPath != "" {
		data["plan_file"] = s.plannerPlanPath
	}
	return s.observer, GoalEvent{Kind: "goal_updated", Data: data}
}

func (s *GoalStore) emitGoalUpdatedLocked(lastEvent string) {
	observer, event := s.goalUpdatedLocked(lastEvent)
	if observer != nil {
		observer.GoalEvent(event)
	}
}

func goalPhase(status string) string {
	switch status {
	case "active", "verifying":
		return "executing"
	default:
		return "idle"
	}
}

func goalWireStatus(status, message, lastEvent string) string {
	switch status {
	case "completed":
		return "complete"
	case "paused":
		if lastEvent == "planning_failed" {
			return "user_paused"
		}
		if strings.Contains(message, "no progress") {
			return "no_progress_paused"
		}
		return "back_off_paused"
	default:
		return status
	}
}

func elapsedMilliseconds(start time.Time) int64 {
	return max(int64(0), time.Since(start).Milliseconds())
}

func effectiveGoalModel(config GoalRoleConfig, role GoalRoleModel) string {
	if role.valid() {
		return role.Model
	}
	return config.CurrentModel
}
