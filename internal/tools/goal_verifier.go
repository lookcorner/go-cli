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
	if r.goal != nil {
		evidence = r.goal.captureEvidence(ctx, snapshot.VerificationRuns)
	}
	prompt := goalVerifierPrompt(snapshot, evidence)
	verdicts := make(chan goalVerdict, count)
	var wait sync.WaitGroup
	for index := range count {
		wait.Add(1)
		go func() {
			defer wait.Done()
			result, err := backend.Start(ctx, SubagentRequest{
				Prompt: prompt, Description: "goal achievement skeptic", Type: "general-purpose",
				Background: false, BackgroundSet: true, CapabilityMode: "read-only",
			})
			if err != nil {
				verdicts <- goalVerdict{Index: index, Verdict: "refuted", Gaps: "goal verifier could not run: " + err.Error()}
				return
			}
			verdict := parseGoalVerdict(result.Output)
			verdict.Index = index
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
	return fmt.Sprintf(`Act as an adversarial completion verifier. Independently inspect the current workspace using read-only tools and test the user's full objective against concrete evidence. Refute completion when any requirement is missing, contradicted, weakly verified, or unverifiable. Do not modify files.

OBJECTIVE:
%s

CHANGES_FILE: %s

CHANGED_FILES:
%s

CANDIDATE SUMMARY:
%s

Return exactly one JSON object and no prose:
{"verdict":"not_refuted","gaps":""}
or
{"verdict":"refuted","gaps":"one concise actionable summary of missing evidence or work"}`, snapshot.Objective, changesPath, changedFiles, snapshot.Message)
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
