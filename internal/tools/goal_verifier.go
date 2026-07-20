package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
)

const goalVerifierGapMaxBytes = 16 << 10

type GoalVerification struct {
	Achieved    bool
	Summary     string
	DetailsPath string
}

type goalVerdict struct {
	Index   int    `json:"-"`
	Verdict string `json:"verdict"`
	Gaps    string `json:"gaps"`
}

func (r *Registry) VerifyGoal(ctx context.Context, snapshot GoalSnapshot, count int) GoalVerification {
	backend := r.subagents.get()
	if backend == nil {
		return GoalVerification{Achieved: true, Summary: "goal verifier unavailable; completion accepted fail-open"}
	}
	if count < 1 {
		count = 1
	} else if count > 5 {
		count = 5
	}
	evidence := goalEvidence{}
	priorSkeptic, priorGaps := "", ""
	if r.goal != nil {
		evidence = r.goal.captureEvidence(ctx, snapshot.VerificationRuns)
		priorSkeptic, priorGaps = r.goal.skepticResumeState()
	}
	prompt := goalVerifierPrompt(snapshot, evidence)
	verdicts := make(chan goalVerdict, count)
	run := func(index int, requestPrompt, resumeFrom string) (goalVerdict, string, error) {
		result, err := backend.Start(ctx, SubagentRequest{
			Prompt: requestPrompt, Description: "goal achievement skeptic", Type: "general-purpose",
			Background: false, BackgroundSet: true, CapabilityMode: "read-only", ResumeFrom: resumeFrom,
		})
		if err != nil {
			return goalVerdict{Index: index, Verdict: "refuted", Gaps: "goal verifier could not run: " + err.Error()}, "", err
		}
		verdict := parseGoalVerdict(result.Output)
		verdict.Index = index
		return verdict, result.ID, nil
	}
	start := 0
	if count > 1 {
		requestPrompt := prompt
		if priorSkeptic != "" {
			requestPrompt = goalVerifierResumePrompt(snapshot, evidence, priorGaps)
		}
		verdict, sessionID, err := run(0, requestPrompt, priorSkeptic)
		if err != nil && priorSkeptic != "" {
			verdict, sessionID, _ = run(0, prompt, "")
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
			verdict, _, _ := run(index, prompt, "")
			verdicts <- verdict
		}()
	}
	wait.Wait()
	close(verdicts)

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
	detailsPath := ""
	if r.goal != nil {
		detailsPath = r.goal.writeVerificationDetails(snapshot.VerificationRuns, all)
	}
	if refuted < count/2+1 {
		return GoalVerification{Achieved: true, Summary: snapshot.Message, DetailsPath: detailsPath}
	}
	sort.Strings(gaps)
	summary := strings.Join(gaps, "; ")
	if summary == "" {
		summary = "independent verification could not confirm that every requirement is complete"
	}
	return GoalVerification{Summary: summary, DetailsPath: detailsPath}
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
	return fmt.Sprintf(`Act as an adversarial completion verifier. Independently inspect the current workspace using read-only tools and test the user's full objective against concrete evidence. Refute completion when any requirement is missing, contradicted, weakly verified, or unverifiable. Do not modify files.

OBJECTIVE:
%s

CHANGES_FILE: %s

CHANGED_FILES:
%s

PLAN_FILE: %s

PLAN_CHANGES:
%s

CANDIDATE SUMMARY:
%s

Return exactly one JSON object and no prose:
{"verdict":"not_refuted","gaps":""}
or
{"verdict":"refuted","gaps":"one concise actionable summary of missing evidence or work"}`, snapshot.Objective, changesPath, changedFiles, planPath, planChanges, snapshot.Message)
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
