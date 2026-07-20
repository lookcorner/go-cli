package tools

const goalVerificationGapsMaxChars = 4000

func (r *Registry) GoalVerificationGaps() string {
	if r == nil || r.goal == nil {
		return ""
	}
	r.goal.mu.Lock()
	gaps := r.goal.lastVerification
	refuted := r.goal.lastClassifierVerdict == "not_achieved" || r.goal.lastClassifierVerdict == "" && gaps != ""
	r.goal.mu.Unlock()
	if !refuted {
		return ""
	}
	return sanitizeGoalDirective(gaps, goalVerificationGapsMaxChars)
}
