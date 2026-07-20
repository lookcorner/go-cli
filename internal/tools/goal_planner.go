package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	goalPlannerMaxBytes = 1 << 20
	goalPlannerFailure  = "Planning failed; resume with /goal to retry."
)

type goalPlannerInput struct {
	objective, workspaceRoot, artifactDir, baselinePath string
}

func (s *GoalStore) plannerInput(enabled bool) (goalPlannerInput, string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !enabled {
		return goalPlannerInput{}, "", false, nil
	}
	if s.status != "active" {
		return goalPlannerInput{}, "", false, errors.New("goal is not active")
	}
	if goalPlanHasContent(s.plannerPlanPath) {
		return goalPlannerInput{}, s.plannerPlanPath, false, nil
	}
	if s.artifactDir == "" {
		return goalPlannerInput{}, "", false, errors.New("goal artifact directory is unavailable")
	}
	baselinePath := ""
	if s.plannerCompleted && goalPlanHasContent(s.planBaselinePath) {
		baselinePath = s.planBaselinePath
	}
	return goalPlannerInput{s.objective, s.workspaceRoot, s.artifactDir, baselinePath}, "", true, nil
}

func (s *GoalStore) finishPlanner(planPath, baselinePath string, runErr error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if runErr != nil {
		s.status, s.message = "user_paused", goalPlannerFailure
		return errors.Join(runErr, s.saveLocked())
	}
	s.plannerPlanPath, s.planBaselinePath, s.plannerCompleted = planPath, baselinePath, true
	if err := s.saveLocked(); err != nil {
		s.status, s.message = "user_paused", goalPlannerFailure
		return errors.Join(err, s.saveLocked())
	}
	return nil
}

func (r *Registry) RunGoalPlanner(ctx context.Context) (string, error) {
	if r.goal == nil {
		return "", errors.New("goal store is unavailable")
	}
	roles := r.goalRoleConfig()
	started := time.Now()
	fired := func() {
		r.goal.setSubagentRole("planner")
		r.emitGoalEvent("goal_planner_fired", map[string]any{
			"attempt": 1, "max_runs": 1, "model_id": effectiveGoalModel(roles, roles.Planner),
		})
		r.emitGoalUpdatedWith("planning_started", map[string]any{"phase": "planning", "planning": true})
	}
	fail := func(reason string, runErr error) (string, error) {
		runErr = r.goal.finishPlanner("", "", runErr)
		r.goal.setSubagentRole("")
		r.emitGoalEvent("goal_planner_fail_closed", map[string]any{
			"reason": reason, "attempt": 1, "latency_ms": elapsedMilliseconds(started),
		})
		r.emitGoalEvent("goal_auto_paused", map[string]any{"reason": "user"})
		r.emitGoalUpdated("planning_failed")
		return "", runErr
	}
	input, existing, run, err := r.goal.plannerInput(roles.PlannerEnabled)
	if err != nil {
		if r.goal.Snapshot().Status == "active" {
			fired()
			return fail("file_write_failed", err)
		}
		return "", err
	}
	if !run {
		return existing, err
	}
	fired()
	backend := r.subagents.get()
	if backend == nil {
		return fail("transport", errors.New("goal planner subagent is unavailable"))
	}
	prompt := goalPlannerPrompt(input.objective)
	request := SubagentRequest{
		Prompt: prompt, Description: "goal plan writer", Type: "general-purpose",
		Background: false, BackgroundSet: true, CapabilityMode: "execute", CWD: input.workspaceRoot,
		Model: roles.Planner.Model, HarnessType: roles.Planner.AgentType,
	}
	result, err := backend.Start(ctx, request)
	r.AddGoalRoleTokens(result.TokensUsed)
	if err != nil && roles.Planner.valid() && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		r.emitGoalEvent("goal_role_model_fail_open", map[string]any{"role": "planner", "reason": "spawn_failed"})
		request.Model, request.HarnessType = "", ""
		result, err = backend.Start(ctx, request)
		r.AddGoalRoleTokens(result.TokensUsed)
	}
	plan := strings.TrimSpace(result.Output)
	if err == nil && (plan == "" || plan == "Done" || len(plan) > goalPlannerMaxBytes) {
		return fail("missing_plan_file", errors.New("goal planner returned no valid plan"))
	}
	if err != nil {
		reason := "runtime"
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			reason = "aborted"
		}
		return fail(reason, err)
	}
	plan += "\n"
	planPath := filepath.Join(input.artifactDir, "goal-plan.md")
	baselinePath := input.baselinePath
	if baselinePath == "" {
		baselinePath = filepath.Join(input.artifactDir, "goal-plan-baseline.md")
		err = writeGoalArtifact(baselinePath, []byte(plan))
	}
	if err == nil {
		err = writeGoalArtifact(planPath, []byte(plan))
	}
	if err != nil {
		return fail("file_write_failed", fmt.Errorf("write goal plan: %w", err))
	}
	if err = r.goal.finishPlanner(planPath, baselinePath, nil); err != nil {
		return fail("file_write_failed", err)
	}
	r.goal.setSubagentRole("")
	r.emitGoalEvent("goal_planner_completed", map[string]any{"attempt": 1, "latency_ms": elapsedMilliseconds(started)})
	r.emitGoalUpdated("planning_completed")
	return planPath, nil
}

func goalPlanHasContent(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular() && info.Size() > 0 && info.Size() <= goalPlannerMaxBytes
}

func goalPlannerPrompt(objective string) string {
	return `Act as the Goal Plan Writer. Inspect the workspace as needed, but do not modify it. Convert the objective into a short, concrete contract for the implementer and independent verifiers. Describe observable outcomes rather than prescribed file names or APIs. Return only Markdown with these sections in order:

# Plan: <headline>
## Goal kind
## Acceptance criteria
## Verification plan
## Non-goals
## Assumed scope
## Implementation approach
## Task checklist

Keep acceptance criteria independently checkable and verification steps executable against shipped behavior. Include at least one non-goal. Verification output paths must use the literal ` + "`{SCRATCH}`" + ` placeholder (for example ` + "`{SCRATCH}/test.log`" + `), never a shared /tmp path. Under Task checklist, write 3-8 ordered ` + "`- [ ]`" + ` steps, each concrete and completable in one turn, ending with testing or evidence. Do not put checkboxes in other sections. Preserve every explicit requirement from the objective.

OBJECTIVE:
` + objective
}
