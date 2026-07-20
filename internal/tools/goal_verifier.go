package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	goalVerifierGapMaxBytes        = 16 << 10
	goalFirstFinalResponseMaxRunes = 4096
)

type GoalVerification struct {
	Achieved    bool
	Verified    bool
	Summary     string
	DetailsPath string
}

type goalVerdict struct {
	Index   int    `json:"-"`
	Verdict string `json:"verdict"`
	Gaps    string `json:"gaps"`
	Latency int64  `json:"-"`
}

func (r *Registry) VerifyGoal(ctx context.Context, snapshot GoalSnapshot, count int) GoalVerification {
	if count < 1 {
		count = 1
	} else if count > 5 {
		count = 5
	}
	started := time.Now()
	roles := r.goalRoleConfig()
	maxRuns := uint32(1)
	if r.goal != nil {
		r.goal.mu.Lock()
		maxRuns = max(uint32(1), r.goal.classifierMaxRuns)
		r.goal.mu.Unlock()
	}
	r.emitGoalEvent("goal_classifier_fired", map[string]any{
		"attempt": snapshot.VerificationRuns, "max_runs": maxRuns,
		"model_id": roles.CurrentModel,
	})
	backend := r.subagents.get()
	if backend == nil {
		r.emitGoalEvent("goal_classifier_fail_open", map[string]any{
			"reason": "sampler_error", "attempt": snapshot.VerificationRuns, "latency_ms": elapsedMilliseconds(started),
		})
		return GoalVerification{Achieved: true, Summary: "goal verifier unavailable; completion accepted fail-open"}
	}
	evidence := goalEvidence{}
	if r.goal != nil {
		evidence = r.goal.captureEvidence(ctx, snapshot.VerificationRuns)
	}
	promptSnapshot := snapshot
	anchor := ""
	if r.goal != nil {
		anchor = r.goal.finalResponseAnchor()
	}
	promptSnapshot.Message, anchor = composeGoalVerifierFinalResponse(anchor, snapshot.Message)
	prompt := goalVerifierPrompt(promptSnapshot, evidence)
	assignments := make([]GoalRoleModel, count)
	if r.goal != nil {
		assignments = r.goal.skepticModelAssignments(roles.Skeptics, count, roles.UseCurrentModelOnly)
	}
	priorSkeptic, priorGaps := "", ""
	if r.goal != nil {
		priorSkeptic, priorGaps = r.goal.skepticResumeState()
	}
	verdicts := make(chan goalVerdict, count)
	run := func(index int, requestPrompt, resumeFrom string, role GoalRoleModel, roleFallback bool) (goalVerdict, string, error) {
		started := time.Now()
		request := SubagentRequest{
			Prompt: requestPrompt, Description: "goal achievement skeptic", Type: "general-purpose",
			Background: false, BackgroundSet: true, CapabilityMode: "read-only", ResumeFrom: resumeFrom,
			Model: role.Model, HarnessType: role.AgentType,
		}
		result, err := backend.Start(ctx, request)
		r.AddGoalRoleTokens(result.TokensUsed)
		if err != nil && roleFallback && role.valid() && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			r.emitGoalEvent("goal_role_model_fail_open", map[string]any{
				"role": "skeptic", "skeptic_idx": index, "reason": "spawn_failed",
			})
			request.Model, request.HarnessType = "", ""
			result, err = backend.Start(ctx, request)
			r.AddGoalRoleTokens(result.TokensUsed)
		}
		if err != nil {
			return goalVerdict{Index: index, Verdict: "refuted", Gaps: "goal verifier could not run: " + err.Error(), Latency: elapsedMilliseconds(started)}, "", err
		}
		verdict := parseGoalVerdict(result.Output)
		verdict.Index = index
		verdict.Latency = elapsedMilliseconds(started)
		return verdict, result.ID, nil
	}
	start := 0
	if count > 1 {
		requestPrompt := prompt
		if priorSkeptic != "" {
			requestPrompt = goalVerifierResumePrompt(promptSnapshot, evidence, priorGaps)
		}
		verdict, sessionID, err := run(0, requestPrompt, priorSkeptic, assignments[0], false)
		if err != nil && priorSkeptic != "" {
			verdict, sessionID, _ = run(0, prompt, "", assignments[0], true)
		}
		verdicts <- verdict
		if r.goal != nil {
			r.goal.recordSkeptic0Session(sessionID, false)
		}
		start = 1
	} else if r.goal != nil {
		r.goal.recordSkeptic0Session("", true)
	}
	var wait sync.WaitGroup
	for index := start; index < count; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			verdict, _, _ := run(index, prompt, "", assignments[index], true)
			verdicts <- verdict
		}()
	}
	wait.Wait()
	close(verdicts)
	if r.goal != nil {
		r.goal.recordFirstFinalResponse(anchor)
	}

	refuted := 0
	gaps := make([]string, 0, count)
	all := make([]goalVerdict, 0, count)
	for verdict := range verdicts {
		all = append(all, verdict)
		if verdict.Verdict != "not_refuted" {
			refuted++
			if gap := strings.TrimSpace(verdict.Gaps); gap != "" {
				gaps = append(gaps, gap)
			}
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Index < all[j].Index })
	for _, verdict := range all {
		r.emitGoalEvent("goal_verifier_skeptic_verdict", map[string]any{
			"attempt": snapshot.VerificationRuns, "skeptic_idx": verdict.Index,
			"refuted": verdict.Verdict != "not_refuted", "confidence": "unknown", "latency_ms": verdict.Latency,
		})
	}
	achieved := refuted < count/2+1
	r.emitGoalEvent("goal_verifier_aggregate_verdict", map[string]any{
		"attempt": snapshot.VerificationRuns, "refuted_count": refuted, "total": count, "achieved": achieved,
	})
	verdictName := "not_achieved"
	if achieved {
		verdictName = "achieved"
	}
	r.emitGoalEvent("goal_classifier_verdict", map[string]any{
		"verdict": verdictName, "attempt": snapshot.VerificationRuns, "latency_ms": elapsedMilliseconds(started),
	})
	detailsPath := ""
	if r.goal != nil {
		detailsPath = r.goal.writeVerificationDetails(snapshot.VerificationRuns, all)
	}
	if achieved {
		return GoalVerification{Achieved: true, Verified: true, Summary: snapshot.Message, DetailsPath: detailsPath}
	}
	sort.Strings(gaps)
	summary := strings.Join(gaps, "; ")
	if summary == "" {
		summary = "independent verification could not confirm that every requirement is complete"
	}
	return GoalVerification{Summary: summary, DetailsPath: detailsPath}
}

func composeGoalVerifierFinalResponse(first, current string) (string, string) {
	if first == "" {
		if strings.TrimSpace(current) == "" {
			return current, ""
		}
		return current, truncateGoalFirstFinalResponse(current)
	}
	note := strings.TrimSpace(current)
	if note == "" || note == strings.TrimSpace(first) {
		return first, ""
	}
	return first + "\n\n## Changes this round\n" + note, ""
}

func truncateGoalFirstFinalResponse(value string) string {
	runes := []rune(value)
	if len(runes) > goalFirstFinalResponseMaxRunes {
		return string(runes[:goalFirstFinalResponseMaxRunes])
	}
	return value
}

func (s *GoalStore) finalResponseAnchor() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.firstFinalResponse
}

func (s *GoalStore) recordFirstFinalResponse(value string) {
	if value == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status == "verifying" && s.firstFinalResponse == "" {
		s.firstFinalResponse = truncateGoalFirstFinalResponse(value)
		_ = s.saveLocked()
	}
}

func goalVerifierResumePrompt(snapshot GoalSnapshot, evidence goalEvidence, priorGaps string) string {
	prompt := goalVerifierPrompt(snapshot, evidence)
	resume := `You are resuming the same skeptic role from the previous verification round. Re-read the current changed files because prior tool results may be stale. Re-check every prior gap against current evidence and reject any regression or unresolved requirement.

PRIOR GAPS:
` + strings.TrimSpace(priorGaps) + "\n\n"
	return strings.Replace(prompt, "Return exactly one JSON object", resume+"Return exactly one JSON object", 1)
}

func goalVerifierPrompt(snapshot GoalSnapshot, evidence goalEvidence) string {
	changesPath := evidence.changesPath
	if changesPath == "" {
		changesPath = "(unavailable)"
	}
	changedFiles := "(none captured)"
	if len(evidence.changedFiles) > 0 {
		lines := make([]string, 0, min(len(evidence.changedFiles), goalEvidenceMaxFiles))
		for _, path := range evidence.changedFiles[:min(len(evidence.changedFiles), goalEvidenceMaxFiles)] {
			lines = append(lines, "- "+sanitizeGoalEvidencePath(path))
		}
		changedFiles = strings.Join(lines, "\n")
	}
	planPath := evidence.planPath
	if planPath == "" {
		planPath = "(unavailable)"
	}
	planChanges := evidence.planChanges
	if planChanges == "" {
		planChanges = "(none)"
	}
	scratch := snapshot.ScratchDir
	if scratch == "" {
		scratch = "(unavailable)"
	}
	scratchStatus := "The private scratch directory was unavailable; do not reject completion solely for missing captured output."
	if snapshot.ScratchReady {
		scratchStatus = "Audit saved evidence in this directory against the plan's verification steps."
	}
	return fmt.Sprintf(`Act as an adversarial completion verifier. Independently inspect the current workspace using read-only tools and test the user's full objective against concrete evidence. Refute completion when any requirement is missing, contradicted, weakly verified, or unverifiable. Do not modify files.

OBJECTIVE:
%s

CHANGES_FILE: %s

CHANGED_FILES:
%s

PLAN_FILE: %s

PLAN_CHANGES:
%s

IMPLEMENTER_SCRATCH: %s
The plan's literal {SCRATCH} placeholder resolves to this directory. %s

CANDIDATE SUMMARY:
%s

Return exactly one JSON object and no prose:
{"verdict":"not_refuted","gaps":""}
or
{"verdict":"refuted","gaps":"one concise actionable summary of missing evidence or work"}`, snapshot.Objective, changesPath, changedFiles, planPath, planChanges, scratch, scratchStatus, snapshot.Message)
}

func parseGoalVerdict(output string) goalVerdict {
	trimmed := strings.TrimSpace(output)
	trimmed = strings.TrimPrefix(trimmed, "```json")
	trimmed = strings.TrimPrefix(trimmed, "```")
	trimmed = strings.TrimSuffix(trimmed, "```")
	var verdict goalVerdict
	if json.Unmarshal([]byte(strings.TrimSpace(trimmed)), &verdict) == nil {
		verdict.Verdict = strings.ToLower(strings.TrimSpace(verdict.Verdict))
		if verdict.Verdict == "refuted" || verdict.Verdict == "not_refuted" {
			verdict.Gaps = strings.TrimSpace(verdict.Gaps)
			if len(verdict.Gaps) > goalVerifierGapMaxBytes {
				verdict.Gaps = truncateUTF8(verdict.Gaps, goalVerifierGapMaxBytes) + "... (truncated)"
			}
			return verdict
		}
	}
	token := strings.Trim(strings.TrimSpace(output), "` .!\n\r\t")
	if token == "Not Refuted" {
		return goalVerdict{Verdict: "not_refuted"}
	}
	return goalVerdict{Verdict: "refuted", Gaps: "verifier returned an invalid verdict"}
}
