package tools

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const goalStateVersion = 1

type durableGoalState struct {
	Version                   int             `json:"version"`
	GoalID                    string          `json:"goal_id"`
	Objective                 string          `json:"objective"`
	Status                    string          `json:"status"`
	Message                   string          `json:"message,omitempty"`
	VerificationRuns          uint32          `json:"verification_runs,omitempty"`
	RoundsSinceVerify         uint32          `json:"rounds_since_verify,omitempty"`
	TotalWorkerRounds         uint32          `json:"total_worker_rounds,omitempty"`
	TotalVerifyRounds         uint32          `json:"total_verify_rounds,omitempty"`
	LastVerification          string          `json:"last_verification,omitempty"`
	StallVerification         string          `json:"stall_verification,omitempty"`
	VerificationStall         int             `json:"verification_stall,omitempty"`
	ConsecutiveReject         uint32          `json:"consecutive_not_achieved,omitempty"`
	StrategistFiredAt         uint32          `json:"last_strategist_fired_at,omitempty"`
	StrategistBonus           uint32          `json:"strategist_cap_bonus,omitempty"`
	StrategyPath              string          `json:"strategy_path,omitempty"`
	StrategyNote              string          `json:"strategy_recommendation,omitempty"`
	BaselineCommit            string          `json:"baseline_commit,omitempty"`
	CreatedAtUnix             int64           `json:"created_at_unix"`
	PlanBaselinePath          string          `json:"plan_baseline_path,omitempty"`
	PlannerPlanPath           string          `json:"plan_file,omitempty"`
	PlannerCompleted          bool            `json:"planner_completed,omitempty"`
	SummaryAttempted          bool            `json:"summary_attempted,omitempty"`
	ClosingSummary            string          `json:"closing_summary,omitempty"`
	TokenBudget               int64           `json:"token_budget,omitempty"`
	TokensUsed                int64           `json:"tokens_used,omitempty"`
	FinishedSubagentTokens    int64           `json:"finished_subagent_tokens,omitempty"`
	CurrentSubagentRole       string          `json:"current_subagent_role,omitempty"`
	LastClassifierVerdict     string          `json:"last_classifier_verdict,omitempty"`
	LastClassifierDetailsPath string          `json:"last_classifier_details_path,omitempty"`
	FirstFinalResponse        string          `json:"first_final_response,omitempty"`
	Skeptic0SessionID         string          `json:"skeptic0_session_id,omitempty"`
	SkepticModels             []GoalRoleModel `json:"skeptic_model_assignment,omitempty"`
}

func (s *GoalStore) saveLocked() error {
	if s.statePath == "" {
		return nil
	}
	data, err := json.Marshal(durableGoalState{
		Version: goalStateVersion, GoalID: s.goalID, Objective: s.objective, Status: s.status, Message: s.message,
		VerificationRuns: s.verificationRuns, RoundsSinceVerify: s.roundsSinceVerify, LastVerification: s.lastVerification,
		TotalWorkerRounds: s.totalWorkerRounds, TotalVerifyRounds: s.totalVerifyRounds,
		StallVerification: s.stallVerification, VerificationStall: s.verificationStall,
		ConsecutiveReject: s.consecutiveReject, StrategistFiredAt: s.strategistFiredAt,
		StrategistBonus: s.strategistBonus, StrategyPath: s.strategyPath, StrategyNote: s.strategyNote,
		BaselineCommit: s.baselineCommit,
		CreatedAtUnix:  s.createdAtUnix, PlanBaselinePath: s.planBaselinePath,
		PlannerPlanPath:           s.plannerPlanPath,
		PlannerCompleted:          s.plannerCompleted,
		SummaryAttempted:          s.summaryAttempted,
		ClosingSummary:            s.closingSummary,
		TokenBudget:               s.tokenBudget,
		TokensUsed:                s.tokensUsed,
		FinishedSubagentTokens:    s.finishedSubagentTokens,
		CurrentSubagentRole:       s.currentSubagentRole,
		LastClassifierVerdict:     s.lastClassifierVerdict,
		LastClassifierDetailsPath: s.lastClassifierDetailsPath,
		FirstFinalResponse:        s.firstFinalResponse,
		Skeptic0SessionID:         s.skeptic0SessionID,
		SkepticModels:             s.skepticModels,
	})
	if err != nil {
		return err
	}
	return writeGoalArtifact(s.statePath, data)
}

func (s *GoalStore) loadState() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	info, statErr := os.Lstat(s.statePath)
	if errors.Is(statErr, os.ErrNotExist) {
		return nil
	}
	if statErr != nil {
		return fmt.Errorf("read goal state: %w", statErr)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return errors.New("goal state must be a regular file no larger than 1 MiB")
	}
	data, err := os.ReadFile(s.statePath)
	if err != nil {
		return fmt.Errorf("read goal state: %w", err)
	}
	var state durableGoalState
	if err := json.Unmarshal(data, &state); err != nil {
		return fmt.Errorf("decode goal state: %w", err)
	}
	if state.Version != goalStateVersion {
		return fmt.Errorf("unsupported goal state version %d", state.Version)
	}
	state.Objective = strings.TrimSpace(state.Objective)
	if state.Objective == "" {
		return errors.New("goal state objective is empty")
	}
	generatedID := false
	if state.GoalID == "" || !validGoalSessionID(state.GoalID) {
		state.GoalID, err = newGoalID()
		if err != nil {
			return err
		}
		generatedID = true
	}
	if !validGoalStatus(state.Status) {
		state.Status = "paused"
		state.Message = "goal state used an unknown status and was paused"
	}
	if !validGoalObjectID(state.BaselineCommit) {
		state.BaselineCommit = ""
	}
	if !validGoalArtifactPath(s.artifactDir, state.PlanBaselinePath) {
		state.PlanBaselinePath = ""
	}
	if !validGoalArtifactPath(s.artifactDir, state.PlannerPlanPath) {
		state.PlannerPlanPath = ""
	}
	if !validGoalArtifactPath(s.artifactDir, state.StrategyPath) {
		state.StrategyPath, state.StrategyNote = "", ""
	}
	if !validGoalArtifactPath(s.artifactDir, state.LastClassifierDetailsPath) {
		state.LastClassifierDetailsPath = ""
	}
	if state.LastClassifierVerdict != "achieved" && state.LastClassifierVerdict != "not_achieved" {
		state.LastClassifierVerdict = ""
	}
	if state.CreatedAtUnix <= 0 {
		state.CreatedAtUnix = time.Now().Unix()
	}
	if state.VerificationStall < 0 {
		state.VerificationStall = 0
	}
	state.TokenBudget, state.TokensUsed = max(int64(0), state.TokenBudget), max(int64(0), state.TokensUsed)
	state.FinishedSubagentTokens = max(int64(0), min(state.FinishedSubagentTokens, state.TokensUsed))
	if state.StrategistBonus != goalStrategistBonus {
		state.StrategistBonus = 0
	}
	state.ClosingSummary = truncateGoalSummary(state.ClosingSummary)
	if state.ClosingSummary != "" {
		state.SummaryAttempted = true
	}
	if !validGoalSessionID(state.Skeptic0SessionID) {
		state.Skeptic0SessionID = ""
	}
	s.goalID, s.objective, s.status, s.message = state.GoalID, state.Objective, state.Status, state.Message
	s.verificationRuns, s.roundsSinceVerify, s.lastVerification = state.VerificationRuns, state.RoundsSinceVerify, state.LastVerification
	s.totalWorkerRounds, s.totalVerifyRounds = state.TotalWorkerRounds, state.TotalVerifyRounds
	s.stallVerification, s.verificationStall = state.StallVerification, state.VerificationStall
	s.consecutiveReject, s.strategistFiredAt = state.ConsecutiveReject, state.StrategistFiredAt
	s.strategistBonus, s.strategyPath, s.strategyNote = state.StrategistBonus, state.StrategyPath, state.StrategyNote
	s.baselineCommit = state.BaselineCommit
	s.createdAtUnix, s.planBaselinePath = state.CreatedAtUnix, state.PlanBaselinePath
	s.plannerPlanPath = state.PlannerPlanPath
	s.plannerCompleted = state.PlannerCompleted
	s.summaryAttempted, s.closingSummary = state.SummaryAttempted, state.ClosingSummary
	s.tokenBudget, s.tokensUsed, s.finishedSubagentTokens = state.TokenBudget, state.TokensUsed, state.FinishedSubagentTokens
	s.currentSubagentRole = strings.TrimSpace(state.CurrentSubagentRole)
	if s.currentSubagentRole != "planner" && s.currentSubagentRole != "verifier" && s.currentSubagentRole != "strategist" {
		s.currentSubagentRole = ""
	}
	s.lastClassifierVerdict, s.lastClassifierDetailsPath = state.LastClassifierVerdict, state.LastClassifierDetailsPath
	s.firstFinalResponse = truncateGoalFirstFinalResponse(state.FirstFinalResponse)
	if s.status != "completed" && s.status != "budget_limited" {
		s.prepareScratchLocked()
	}
	s.skeptic0SessionID = state.Skeptic0SessionID
	s.skepticModels = validGoalRoleModels(state.SkepticModels)
	if generatedID {
		return s.saveLocked()
	}
	return nil
}

func (s *GoalStore) Resume() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.objective == "" {
		return "", errors.New("no persisted goal is available")
	}
	if s.status == "completed" {
		return "", errors.New("completed goal cannot be resumed")
	}
	if s.status == "budget_limited" {
		return "", errors.New("budget-limited goal cannot be resumed")
	}
	s.status, s.message = "active", ""
	s.currentSubagentRole = ""
	s.verificationRuns, s.verificationStall = 0, 0
	s.stallVerification = ""
	s.resetStrategistLocked()
	s.prepareScratchLocked()
	return s.objective, s.saveLocked()
}

func (s *GoalStore) skepticResumeState() (string, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.skeptic0SessionID, s.lastVerification
}

func (s *GoalStore) recordSkeptic0Session(id string, clear bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if clear || id != "" {
		s.skeptic0SessionID = id
		_ = s.saveLocked()
	}
}

func (s *GoalStore) skepticModelAssignments(pool []GoalRoleModel, count int, useCurrentOnly bool) []GoalRoleModel {
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := false
	if useCurrentOnly && len(s.skepticModels) > 0 {
		s.skepticModels = nil
		changed = true
	}
	if useCurrentOnly && s.skeptic0SessionID != "" {
		s.skeptic0SessionID = ""
		changed = true
	}
	if !useCurrentOnly && len(pool) > 0 {
		for len(s.skepticModels) < count {
			s.skepticModels = append(s.skepticModels, pool[len(s.skepticModels)%len(pool)])
			changed = true
		}
	}
	if changed {
		_ = s.saveLocked()
	}
	result := make([]GoalRoleModel, count)
	copy(result, s.skepticModels)
	return result
}

func validGoalRoleModels(models []GoalRoleModel) []GoalRoleModel {
	result := make([]GoalRoleModel, 0, len(models))
	for _, model := range models {
		model.Model, model.AgentType = strings.TrimSpace(model.Model), strings.TrimSpace(model.AgentType)
		if model.Model != "" && validGoalAgentType(model.AgentType) {
			result = append(result, model)
		}
	}
	return result
}

func validGoalAgentType(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, char := range value {
		if char < ' ' || char == 0x7f {
			return false
		}
	}
	return true
}

func validGoalStatus(status string) bool {
	switch status {
	case "active", "verifying", "paused", "blocked", "completed", "budget_limited":
		return true
	default:
		return false
	}
}

func validGoalObjectID(value string) bool {
	if value == "" {
		return true
	}
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validGoalSessionID(value string) bool {
	if value == "" {
		return true
	}
	if len(value) > 128 {
		return false
	}
	for index, char := range value {
		alphanumeric := (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9')
		if !alphanumeric && (index == 0 || char != '.' && char != '-' && char != '_') {
			return false
		}
	}
	return true
}

func validGoalArtifactPath(root, path string) bool {
	if path == "" {
		return true
	}
	if !filepath.IsAbs(path) || !pathWithin(root, path) {
		return false
	}
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular()
}
