package tools

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

const goalSummaryMaxChars = 1200

type goalSummaryInput struct {
	objective, workspaceRoot, planPath, detailsPath string
	attempt                                         uint32
}

func (s *GoalStore) claimSummarizer(enabled, verified bool, detailsPath string) (goalSummaryInput, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !enabled || !verified || s.status != "completed" || s.summaryAttempted {
		return goalSummaryInput{}, false
	}
	s.summaryAttempted = true
	if s.saveLocked() != nil {
		s.summaryAttempted = false
		return goalSummaryInput{}, false
	}
	planPath := s.plannerPlanPath
	if !goalPlanHasContent(planPath) && s.workspaceRoot != "" {
		planPath = filepath.Join(s.workspaceRoot, filepath.FromSlash(planFile))
	}
	return goalSummaryInput{
		objective: s.objective, workspaceRoot: s.workspaceRoot, planPath: planPath,
		detailsPath: detailsPath, attempt: s.verificationRuns,
	}, true
}

func (s *GoalStore) finishSummarizer(summary string) {
	s.mu.Lock()
	s.closingSummary = summary
	_ = s.saveLocked()
	s.mu.Unlock()
}

func (r *Registry) RunGoalSummarizer(ctx context.Context, verification GoalVerification) string {
	if r.goal == nil {
		return ""
	}
	roles := r.goalRoleConfig()
	input, ok := r.goal.claimSummarizer(
		roles.SummaryEnabled,
		verification.Achieved && verification.Verified,
		verification.DetailsPath,
	)
	if !ok {
		return ""
	}
	started := time.Now()
	r.emitGoalEvent("goal_summarizer_fired", map[string]any{
		"attempt": input.attempt, "model_id": roles.CurrentModel,
	})
	fail := func(reason string) string {
		r.emitGoalEvent("goal_summarizer_fail_open", map[string]any{
			"reason": reason, "attempt": input.attempt, "latency_ms": elapsedMilliseconds(started),
		})
		return ""
	}
	backend := r.subagents.get()
	if backend == nil {
		return fail("transport")
	}
	result, err := backend.Start(ctx, SubagentRequest{
		Prompt: goalSummarizerPrompt(input), Description: "goal summarizer", Type: "general-purpose",
		Background: false, BackgroundSet: true, CapabilityMode: "read-only", CWD: input.workspaceRoot,
	})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return fail("aborted")
		}
		return fail("runtime")
	}
	summary := truncateGoalSummary(result.Output)
	if summary == "" {
		return fail("empty_summary")
	}
	r.goal.finishSummarizer(summary)
	r.emitGoalEvent("goal_summarizer_completed", map[string]any{
		"attempt": input.attempt, "latency_ms": elapsedMilliseconds(started),
	})
	return summary
}

func truncateGoalSummary(value string) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) > goalSummaryMaxChars {
		value = string(runes[:goalSummaryMaxChars]) + " [...]"
	}
	return value
}

func goalSummarizerPrompt(input goalSummaryInput) string {
	planPath, detailsPath := input.planPath, input.detailsPath
	if planPath == "" {
		planPath = "(unavailable)"
	}
	if detailsPath == "" {
		detailsPath = "(unavailable)"
	}
	return fmt.Sprintf(`Act as the Goal Summarizer. The goal was independently verified as achieved. Inspect the delivered workspace using read, search, and list tools only. Do not edit files and do not run commands.

Write the single closing message the user reads. Output only Markdown:
1. One sentence naming what was delivered.
2. The exact command or short steps needed to use it, on one line or in at most 3 bullets.

HARD LIMIT: at most 80 words and at most 4 bullets. Omit detail rather than exceed the limit. Do not echo verifier review details.

OBJECTIVE:
%s

PLAN_FILE: %s
DETAILS_FILE: %s
SESSION_TRACES_DIR: (unavailable)`, input.objective, sanitizeGoalEvidencePath(planPath), sanitizeGoalEvidencePath(detailsPath))
}
