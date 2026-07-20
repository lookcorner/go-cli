package tools

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/lookcorner/go-cli/internal/api"
)

type GoalSnapshot struct {
	GoalID                    string
	Objective                 string
	Status                    string
	Message                   string
	VerificationRuns          uint32
	PlanPath                  string
	ClosingSummary            string
	TokenBudget               int64
	TokensUsed                int64
	RoundsSinceVerify         uint32
	TotalWorkerRounds         uint32
	TotalVerifyRounds         uint32
	FinishedSubagentTokens    int64
	LastClassifierVerdict     string
	LastClassifierDetailsPath string
}

type GoalRoleModel struct {
	Model     string `json:"model"`
	AgentType string `json:"agent_type"`
}

type GoalRoleConfig struct {
	CurrentModel        string
	ClassifierMaxRuns   uint32
	PlannerEnabled      bool
	SummaryEnabled      bool
	StrategistEvery     uint32
	UseCurrentModelOnly bool
	Planner             GoalRoleModel
	Strategist          GoalRoleModel
	Skeptics            []GoalRoleModel
}

type GoalStore struct {
	mu                        sync.Mutex
	goalID                    string
	objective                 string
	status                    string
	message                   string
	verificationRuns          uint32
	totalWorkerRounds         uint32
	totalVerifyRounds         uint32
	roundsSinceVerify         uint32
	lastVerification          string
	stallVerification         string
	verificationStall         int
	consecutiveReject         uint32
	strategistFiredAt         uint32
	strategistBonus           uint32
	strategyPath              string
	strategyNote              string
	workspaceRoot             string
	artifactDir               string
	baselineCommit            string
	createdAtUnix             int64
	planBaselinePath          string
	plannerPlanPath           string
	plannerCompleted          bool
	summaryAttempted          bool
	closingSummary            string
	observer                  GoalObserver
	classifierMaxRuns         uint32
	tokenBudget               int64
	tokensUsed                int64
	finishedSubagentTokens    int64
	currentSubagentRole       string
	lastClassifierVerdict     string
	lastClassifierDetailsPath string
	statePath                 string
	skeptic0SessionID         string
	skepticModels             []GoalRoleModel
}

func NewGoalStore() *GoalStore { return &GoalStore{} }

func (s *GoalStore) Begin(objective string) error { return s.BeginWithBudget(objective, 0) }

func (s *GoalStore) BeginWithBudget(objective string, tokenBudget int64) error {
	objective = strings.TrimSpace(objective)
	if objective == "" {
		return errors.New("goal objective must not be empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status == "active" || s.status == "verifying" {
		return errors.New("a goal is already active")
	}
	goalID, err := newGoalID()
	if err != nil {
		return err
	}
	s.goalID = goalID
	s.objective = objective
	s.status = "active"
	s.message = ""
	s.verificationRuns, s.roundsSinceVerify, s.totalWorkerRounds, s.totalVerifyRounds = 0, 0, 0, 0
	s.lastVerification, s.verificationStall = "", 0
	s.stallVerification = ""
	s.resetStrategistLocked()
	s.skeptic0SessionID = ""
	s.skepticModels = nil
	s.createdAtUnix = time.Now().Unix()
	s.baselineCommit = captureGoalBaseline(s.workspaceRoot)
	s.planBaselinePath = captureGoalPlanBaseline(s.workspaceRoot, s.artifactDir)
	s.plannerPlanPath = ""
	s.plannerCompleted = false
	s.summaryAttempted, s.closingSummary = false, ""
	s.tokenBudget, s.tokensUsed, s.finishedSubagentTokens = max(int64(0), tokenBudget), 0, 0
	s.currentSubagentRole = ""
	s.lastClassifierVerdict, s.lastClassifierDetailsPath = "", ""
	return s.saveLocked()
}

func (s *GoalStore) Snapshot() GoalSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return GoalSnapshot{
		GoalID:    s.goalID,
		Objective: s.objective, Status: s.status, Message: s.message, VerificationRuns: s.verificationRuns,
		PlanPath: s.plannerPlanPath, ClosingSummary: s.closingSummary, TokenBudget: s.tokenBudget, TokensUsed: s.tokensUsed,
		RoundsSinceVerify: s.roundsSinceVerify,
		TotalWorkerRounds: s.totalWorkerRounds, TotalVerifyRounds: s.totalVerifyRounds,
		FinishedSubagentTokens: s.finishedSubagentTokens,
		LastClassifierVerdict:  s.lastClassifierVerdict, LastClassifierDetailsPath: s.lastClassifierDetailsPath,
	}
}

func (s *GoalStore) addTokens(tokens int64, subagent bool) bool {
	if tokens <= 0 {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status != "active" && s.status != "verifying" && s.status != "paused" {
		return false
	}
	if math.MaxInt64-s.tokensUsed < tokens {
		s.tokensUsed = math.MaxInt64
	} else {
		s.tokensUsed += tokens
	}
	if subagent {
		if math.MaxInt64-s.finishedSubagentTokens < tokens {
			s.finishedSubagentTokens = math.MaxInt64
		} else {
			s.finishedSubagentTokens += tokens
		}
	}
	_ = s.saveLocked()
	return true
}

func (s *GoalStore) setSubagentRole(role string) {
	s.mu.Lock()
	s.currentSubagentRole = role
	_ = s.saveLocked()
	s.mu.Unlock()
}

func newGoalID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate goal id: %w", err)
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", value[:4], value[4:6], value[6:8], value[8:10], value[10:]), nil
}

func (s *GoalStore) StartVerification(maxRuns uint32) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status != "verifying" {
		return errors.New("goal is not awaiting verification")
	}
	if s.verificationRuns >= goalClassifierLimit(maxRuns, s.strategistBonus) {
		s.status = "paused"
		err := fmt.Errorf("goal verification paused after %d attempts", s.verificationRuns)
		return errors.Join(err, s.saveLocked())
	}
	s.verificationRuns++
	if s.totalVerifyRounds < ^uint32(0) {
		s.totalVerifyRounds++
	}
	s.roundsSinceVerify = 0
	return s.saveLocked()
}

func (s *GoalStore) ResolveVerification(verification GoalVerification, maxRuns uint32) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status != "verifying" {
		return errors.New("goal is not awaiting verification")
	}
	s.message = strings.TrimSpace(verification.Summary)
	s.lastClassifierVerdict = "not_achieved"
	if verification.Achieved {
		s.lastClassifierVerdict = "achieved"
	}
	if verification.DetailsPath != "" && validGoalArtifactPath(s.artifactDir, verification.DetailsPath) {
		s.lastClassifierDetailsPath = verification.DetailsPath
	}
	if verification.Achieved {
		s.status = "completed"
		s.currentSubagentRole = ""
		s.lastVerification = ""
		s.skeptic0SessionID = ""
		s.skepticModels = nil
		s.resetStrategistLocked()
		return s.saveLocked()
	}
	s.lastVerification = s.message
	s.consecutiveReject++
	if s.message == s.stallVerification {
		s.verificationStall++
	} else {
		s.stallVerification, s.verificationStall = s.message, 1
	}
	stallLimit := 2 + int(s.strategistBonus)
	if s.verificationRuns >= goalClassifierLimit(maxRuns, s.strategistBonus) {
		s.status = "paused"
		s.message = fmt.Sprintf("verification run cap reached after %d attempts: %s", s.verificationRuns, s.message)
	} else if s.verificationStall >= stallLimit {
		s.status = "paused"
		s.message = fmt.Sprintf("verification found no progress across %d attempts: %s", stallLimit, s.message)
	} else {
		s.status = "active"
	}
	return s.saveLocked()
}

func goalClassifierLimit(base, bonus uint32) uint32 {
	base = max(uint32(1), base)
	if ^uint32(0)-base < bonus {
		return ^uint32(0)
	}
	return base + bonus
}

func (s *GoalStore) resetStrategistLocked() {
	s.consecutiveReject, s.strategistFiredAt, s.strategistBonus = 0, 0, 0
	s.strategyPath, s.strategyNote = "", ""
}

type updateGoalTool struct{ store *GoalStore }

func (t *updateGoalTool) Definition() api.ToolDefinition {
	return api.ToolDefinition{
		Type: "function", Name: "update_goal",
		Description: "Report progress on the active goal. Mark completed only when fully achieved, or set blocked_reason only when genuinely stuck.",
		Parameters: objectSchema(map[string]any{
			"completed":      map[string]any{"type": "boolean"},
			"message":        map[string]any{"type": "string"},
			"blocked_reason": map[string]any{"type": "string"},
		}),
	}
}

func (t *updateGoalTool) Execute(_ context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		Completed     *bool  `json:"completed"`
		Message       string `json:"message"`
		BlockedReason string `json:"blocked_reason"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("decode update_goal arguments: %w", err)
	}
	if args.Completed != nil && *args.Completed && strings.TrimSpace(args.BlockedReason) != "" {
		return "", errors.New("completed and blocked_reason are mutually exclusive")
	}
	if args.Completed == nil && strings.TrimSpace(args.Message) == "" && strings.TrimSpace(args.BlockedReason) == "" {
		return "", errors.New("provide completed, message, or blocked_reason")
	}
	t.store.mu.Lock()
	defer t.store.mu.Unlock()
	if t.store.status != "active" {
		return "", errors.New("goal harness is not active; start gork with --goal before calling update_goal")
	}
	if blocked := strings.TrimSpace(args.BlockedReason); blocked != "" {
		t.store.status = "blocked"
		t.store.message = blocked
		if err := t.store.saveLocked(); err != nil {
			return "", err
		}
		t.store.emitGoalUpdatedLocked("goal_blocked")
		return "success: true\nsummary: Goal marked blocked: " + blocked, nil
	}
	if args.Completed != nil && *args.Completed {
		t.store.status = "verifying"
		t.store.message = strings.TrimSpace(args.Message)
		summary := t.store.message
		if summary == "" {
			summary = "Goal completion requested"
		}
		if err := t.store.saveLocked(); err != nil {
			return "", err
		}
		t.store.emitGoalUpdatedLocked("completion_requested")
		return "success: true\nsummary: Awaiting independent verification: " + summary, nil
	}
	t.store.message = strings.TrimSpace(args.Message)
	if err := t.store.saveLocked(); err != nil {
		return "", err
	}
	t.store.emitGoalUpdatedLocked("progress_recorded")
	return "success: true\nsummary: Progress recorded: " + t.store.message, nil
}
