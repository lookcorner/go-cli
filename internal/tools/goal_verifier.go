package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

type GoalVerification struct {
	Achieved bool
	Summary  string
}

type goalVerdict struct {
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
	prompt := goalVerifierPrompt(snapshot)
	verdicts := make(chan goalVerdict, count)
	var wait sync.WaitGroup
	for range count {
		wait.Add(1)
		go func() {
			defer wait.Done()
			result, err := backend.Start(ctx, SubagentRequest{
				Prompt: prompt, Description: "goal achievement skeptic", Type: "general-purpose",
				Background: false, BackgroundSet: true, CapabilityMode: "read-only",
			})
			if err != nil {
				verdicts <- goalVerdict{Verdict: "refuted", Gaps: "goal verifier could not run: " + err.Error()}
				return
			}
			verdicts <- parseGoalVerdict(result.Output)
		}()
	}
	wait.Wait()
	close(verdicts)

	refuted := 0
	gaps := make([]string, 0, count)
	for verdict := range verdicts {
		if verdict.Verdict != "not_refuted" {
			refuted++
			if gap := strings.TrimSpace(verdict.Gaps); gap != "" {
				gaps = append(gaps, gap)
			}
		}
	}
	if refuted < count/2+1 {
		return GoalVerification{Achieved: true, Summary: snapshot.Message}
	}
	summary := strings.Join(gaps, "; ")
	if summary == "" {
		summary = "independent verification could not confirm that every requirement is complete"
	}
	return GoalVerification{Summary: summary}
}

func goalVerifierPrompt(snapshot GoalSnapshot) string {
	return fmt.Sprintf(`Act as an adversarial completion verifier. Independently inspect the current workspace using read-only tools and test the user's full objective against concrete evidence. Refute completion when any requirement is missing, contradicted, weakly verified, or unverifiable. Do not modify files.

OBJECTIVE:
%s

CANDIDATE SUMMARY:
%s

Return exactly one JSON object and no prose:
{"verdict":"not_refuted","gaps":""}
or
{"verdict":"refuted","gaps":"one concise actionable summary of missing evidence or work"}`, snapshot.Objective, snapshot.Message)
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
			return verdict
		}
	}
	token := strings.Trim(strings.TrimSpace(output), "` .!\n\r\t")
	if token == "Not Refuted" {
		return goalVerdict{Verdict: "not_refuted"}
	}
	return goalVerdict{Verdict: "refuted", Gaps: "verifier returned an invalid verdict"}
}
