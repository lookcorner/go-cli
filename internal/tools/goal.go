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
	Objective string
	Status    string
	Message   string
}

type GoalStore struct {
	mu        sync.Mutex
	objective string
	status    string
	message   string
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
	return nil
}

func (s *GoalStore) Snapshot() GoalSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return GoalSnapshot{Objective: s.objective, Status: s.status, Message: s.message}
}

func (s *GoalStore) ResolveVerification(achieved bool, message string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status != "verifying" {
		return errors.New("goal is not awaiting verification")
	}
	s.message = strings.TrimSpace(message)
	if achieved {
		s.status = "completed"
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
