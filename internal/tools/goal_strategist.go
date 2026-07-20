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
	goalStrategistBonus    = 3
	goalStrategyMaxChars   = 4096
	goalPlanSnapshotMaxLen = 1 << 20
)

type goalStrategistInput struct {
	objective, gaps, workspaceRoot, artifactDir string
	consecutive                                 uint32
	attempt                                     uint32
}

func (s *GoalStore) claimStrategist(every uint32) (goalStrategistInput, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	every = max(uint32(1), every)
	if s.status != "active" || s.consecutiveReject < s.strategistFiredAt || s.consecutiveReject-s.strategistFiredAt < every {
		return goalStrategistInput{}, false
	}
	s.strategistFiredAt = s.consecutiveReject
	s.strategistBonus = goalStrategistBonus
	s.stallVerification, s.verificationStall = "", 0
	if s.saveLocked() != nil {
		s.strategistBonus = 0
		return goalStrategistInput{}, false
	}
	return goalStrategistInput{
		objective: s.objective, gaps: s.lastVerification, workspaceRoot: s.workspaceRoot,
		artifactDir: s.artifactDir, consecutive: s.consecutiveReject, attempt: s.verificationRuns,
	}, true
}

func (s *GoalStore) resolveStrategist(path, note string, achieved bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if achieved {
		s.strategyPath, s.strategyNote = path, note
	} else {
		s.strategistBonus = 0
	}
	_ = s.saveLocked()
}

func (r *Registry) RunGoalStrategist(ctx context.Context) string {
	if r.goal == nil {
		return ""
	}
	roles := r.goalRoleConfig()
	input, ok := r.goal.claimStrategist(roles.StrategistEvery)
	if !ok {
		return ""
	}
	started := time.Now()
	r.emitGoalEvent("goal_strategist_fired", map[string]any{
		"attempt": input.attempt, "consecutive_failures": input.consecutive,
		"every": max(uint32(1), roles.StrategistEvery), "model_id": effectiveGoalModel(roles, roles.Strategist),
	})
	fail := func(reason string) string {
		r.goal.resolveStrategist("", "", false)
		r.emitGoalEvent("goal_strategist_failed", map[string]any{
			"reason": reason, "attempt": input.attempt, "consecutive_failures": input.consecutive,
			"latency_ms": elapsedMilliseconds(started),
		})
		return ""
	}
	backend := r.subagents.get()
	if backend == nil {
		return fail("transport")
	}
	if input.artifactDir == "" {
		return fail("missing_strategy_file")
	}
	planPath := ""
	if input.workspaceRoot != "" {
		planPath = filepath.Join(input.workspaceRoot, filepath.FromSlash(planFile))
	}
	plan := captureGoalPlan(planPath)
	defer plan.restore()
	prompt := fmt.Sprintf(`Act as a goal strategist after %d consecutive failed verification rounds. Diagnose the structural reason the implementation is not converging. Independently inspect the current workspace, session artifacts, changed files, and tests. You may run diagnostic commands, but do not modify any workspace file or the acceptance plan.

OBJECTIVE:
%s

LATEST VERIFIER GAPS:
%s

SESSION ARTIFACTS: %s
PLAN FILE: %s

Return only a short Markdown note with these headings:
# Strategy: why the goal is stuck and how to unstick it
## Diagnosis
## Recommended restructure
## Why this converges`, input.consecutive, input.objective, input.gaps, sanitizeGoalEvidencePath(input.artifactDir), sanitizeGoalEvidencePath(plan.path))
	request := SubagentRequest{
		Prompt: prompt, Description: "goal strategist", Type: "general-purpose",
		Background: false, BackgroundSet: true, CapabilityMode: "execute",
		Model: roles.Strategist.Model, HarnessType: roles.Strategist.AgentType,
	}
	result, err := backend.Start(ctx, request)
	if err != nil && roles.Strategist.valid() && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		r.emitGoalEvent("goal_role_model_fail_open", map[string]any{"role": "strategist", "reason": "spawn_failed"})
		request.Model, request.HarnessType = "", ""
		result, err = backend.Start(ctx, request)
	}
	note := truncateGoalStrategy(result.Output)
	path := filepath.Join(input.artifactDir, "goal-strategy.md")
	if err != nil {
		reason := "runtime"
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			reason = "aborted"
		}
		return fail(reason)
	}
	if note == "" || writeGoalArtifact(path, []byte(note+"\n")) != nil {
		return fail("missing_strategy_file")
	}
	r.goal.resolveStrategist(path, note, true)
	r.emitGoalEvent("goal_strategist_completed", map[string]any{
		"attempt": input.attempt, "consecutive_failures": input.consecutive, "latency_ms": elapsedMilliseconds(started),
	})
	return fmt.Sprintf("Strategist recommendation (%s):\n%s", path, note)
}

func truncateGoalStrategy(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || value == "Done" {
		return ""
	}
	runes := []rune(value)
	if len(runes) > goalStrategyMaxChars {
		value = string(runes[:goalStrategyMaxChars]) + "\n\n... (strategy truncated)"
	}
	return value
}

type goalPlanSnapshot struct {
	path   string
	data   []byte
	mode   os.FileMode
	exists bool
	safe   bool
}

func captureGoalPlan(path string) goalPlanSnapshot {
	snapshot := goalPlanSnapshot{path: path}
	if path == "" {
		return snapshot
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		snapshot.safe = true
		return snapshot
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > goalPlanSnapshotMaxLen {
		return snapshot
	}
	data, err := os.ReadFile(path)
	if err == nil {
		snapshot.data, snapshot.mode, snapshot.exists, snapshot.safe = data, info.Mode().Perm(), true, true
	}
	return snapshot
}

func (s goalPlanSnapshot) restore() {
	if !s.safe {
		return
	}
	info, err := os.Lstat(s.path)
	if s.exists {
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return
		}
		if err == nil || os.IsNotExist(err) {
			_ = os.WriteFile(s.path, s.data, s.mode)
		}
		return
	}
	if err == nil && info.Mode().IsRegular() {
		_ = os.Remove(s.path)
	}
}
