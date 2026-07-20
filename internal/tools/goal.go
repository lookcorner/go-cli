package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/lookcorner/go-cli/internal/api"
)

type GoalSnapshot struct {
	Objective        string
	Status           string
	Message          string
	VerificationRuns uint32
}

type GoalStore struct {
	mu                sync.Mutex
	objective         string
	status            string
	message           string
	verificationRuns  uint32
	lastVerification  string
	verificationStall int
	workspaceRoot     string
	artifactDir       string
	baselineCommit    string
}

func NewGoalStore() *GoalStore { return &GoalStore{} }

func (s *GoalStore) Begin(objective string) error {
	objective = strings.TrimSpace(objective)
	if objective == "" {
		return errors.New("goal objective must not be empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status == "active" {
		return errors.New("a goal is already active")
	}
	s.objective = objective
	s.status = "active"
	s.message = ""
	s.verificationRuns, s.lastVerification, s.verificationStall = 0, "", 0
	s.baselineCommit = captureGoalBaseline(s.workspaceRoot)
	return nil
}

func (s *GoalStore) Snapshot() GoalSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return GoalSnapshot{Objective: s.objective, Status: s.status, Message: s.message, VerificationRuns: s.verificationRuns}
}

func (s *GoalStore) StartVerification(maxRuns uint32) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status != "verifying" {
		return errors.New("goal is not awaiting verification")
	}
	maxRuns = max(uint32(1), maxRuns)
	if s.verificationRuns >= maxRuns {
		s.status = "paused"
		return fmt.Errorf("goal verification paused after %d attempts", s.verificationRuns)
	}
	s.verificationRuns++
	return nil
}

func (s *GoalStore) ResolveVerification(achieved bool, message string, maxRuns uint32) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status != "verifying" {
		return errors.New("goal is not awaiting verification")
	}
	s.message = strings.TrimSpace(message)
	if achieved {
		s.status = "completed"
		return nil
	}
	if s.message == s.lastVerification {
		s.verificationStall++
	} else {
		s.lastVerification, s.verificationStall = s.message, 1
	}
	if s.verificationRuns >= max(uint32(1), maxRuns) {
		s.status = "paused"
		s.message = fmt.Sprintf("verification run cap reached after %d attempts: %s", s.verificationRuns, s.message)
	} else if s.verificationStall >= 2 {
		s.status = "paused"
		s.message = "verification found no progress across two attempts: " + s.message
	} else {
		s.status = "active"
	}
	return nil
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
		return "success: true\nsummary: Goal marked blocked: " + blocked, nil
	}
	if args.Completed != nil && *args.Completed {
		t.store.status = "verifying"
		t.store.message = strings.TrimSpace(args.Message)
		summary := t.store.message
		if summary == "" {
			summary = "Goal completion requested"
		}
		return "success: true\nsummary: Awaiting independent verification: " + summary, nil
	}
	t.store.message = strings.TrimSpace(args.Message)
	return "success: true\nsummary: Progress recorded: " + t.store.message, nil
}
