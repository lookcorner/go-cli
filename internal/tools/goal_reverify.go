package tools

import "fmt"

func (r *Registry) GoalReverifyReminder(threshold uint32) (string, error) {
	if r == nil || r.goal == nil {
		return "", nil
	}
	threshold = max(uint32(1), threshold)
	r.goal.mu.Lock()
	if r.goal.status != "active" {
		r.goal.mu.Unlock()
		return "", nil
	}
	if r.goal.roundsSinceVerify < ^uint32(0) {
		r.goal.roundsSinceVerify++
	}
	rounds := r.goal.roundsSinceVerify
	refuted := r.goal.consecutiveReject > 0 || r.goal.lastVerification != ""
	err := r.goal.saveLocked()
	r.goal.mu.Unlock()
	if err != nil || !refuted || rounds < threshold {
		return "", err
	}
	hardAt := threshold * 3
	if threshold > ^uint32(0)/3 {
		hardAt = ^uint32(0)
	}
	lead := "Re-verify before continuing."
	if rounds >= hardAt {
		lead = "STOP DRIFTING \u2014 RE-VERIFY NOW."
	}
	return fmt.Sprintf("%s You have run %d rounds since your last verification without calling `update_goal(completed: true)`. The ONLY way to finish this goal is to PASS verification - not to keep editing. If the plan's `## Verification plan` steps now hold, call `update_goal(completed: true)` THIS round to re-trigger the skeptic panel. If they do NOT, name the SINGLE concrete gap still blocking it and fix exactly that - do not make cosmetic changes to look busy.", lead, rounds), nil
}
