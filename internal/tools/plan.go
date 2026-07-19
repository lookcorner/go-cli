package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/lookcorner/go-cli/internal/api"
	"github.com/lookcorner/go-cli/internal/workspace"
)

const planFile = ".grok/plan.md"

type PlanModeEvent struct {
	ToolCallID  string
	PlanPath    string
	PlanContent string
}

type PlanModeDecision struct {
	Outcome  string `json:"outcome"`
	Feedback string `json:"feedback,omitempty"`
}

type PlanModeObserver interface {
	PlanModeEntered(PlanModeEvent)
	ApprovePlanModeExit(context.Context, PlanModeEvent) (PlanModeDecision, error)
	PlanModeExited(PlanModeEvent)
}

type PlanMode struct {
	ws       *workspace.Workspace
	approver Approver
	mu       sync.RWMutex
	active   bool
	state    string
	observer PlanModeObserver
}

func NewPlanMode(ws *workspace.Workspace, approver Approver) *PlanMode {
	return &PlanMode{ws: ws, approver: approver}
}

func (m *PlanMode) Configure(artifactDir string) error {
	if artifactDir == "" {
		return errors.New("plan mode artifact directory is required")
	}
	path := filepath.Join(artifactDir, "plan_mode.json")
	var state struct {
		Active bool `json:"active"`
	}
	data, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(data, &state); err != nil {
			return fmt.Errorf("decode plan mode state: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read plan mode state: %w", err)
	}
	m.mu.Lock()
	m.state, m.active = path, state.Active
	m.mu.Unlock()
	return nil
}

func (m *PlanMode) SetObserver(observer PlanModeObserver) {
	m.mu.Lock()
	m.observer = observer
	m.mu.Unlock()
}

func (m *PlanMode) Active() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.active
}

func (m *PlanMode) SetActive(active bool) error {
	m.mu.Lock()
	previous := m.active
	m.active = active
	if err := m.saveLocked(); err != nil {
		m.active = previous
		m.mu.Unlock()
		return err
	}
	m.mu.Unlock()
	return nil
}

func (m *PlanMode) Instructions() string {
	if !m.Active() {
		return ""
	}
	return "Plan mode is active. Investigate the codebase and produce a concrete implementation plan. Do not modify the workspace except for .grok/plan.md. Keep the plan file current, then call exit_plan_mode when it is ready for approval."
}

func (m *PlanMode) Allow(name string, raw json.RawMessage, tool Tool) error {
	if !m.Active() || name == "enter_plan_mode" || name == "exit_plan_mode" {
		return nil
	}
	if path := mutationPath(name, raw); path != "" {
		if m.isPlanPath(path) {
			return nil
		}
		return fmt.Errorf("plan mode only allows workspace edits to %s", planFile)
	}
	switch name {
	case "shell", "run_terminal_cmd", "start_background_command", "kill_background_command", "monitor", "kill_task", "scheduler_create", "scheduler_delete", "task", "image_gen", "image_edit", "image_to_video", "reference_to_video":
		return fmt.Errorf("tool %q is unavailable while plan mode is active", name)
	}
	if _, mcpTool := tool.(interface{ MCPServerName() string }); mcpTool {
		return fmt.Errorf("MCP tool %q is unavailable while plan mode is active", name)
	}
	return nil
}

func (m *PlanMode) isPlanPath(path string) bool {
	if filepath.IsAbs(path) {
		relative, err := filepath.Rel(m.ws.Root(), filepath.Clean(path))
		return err == nil && filepath.ToSlash(relative) == planFile
	}
	return filepath.ToSlash(filepath.Clean(path)) == planFile
}

func (m *PlanMode) seedPlan() (string, error) {
	path := filepath.Join(m.ws.Root(), filepath.FromSlash(planFile))
	info, err := os.Lstat(path)
	if err == nil {
		if !info.Mode().IsRegular() {
			return path, errors.New("plan path exists but is not a regular file")
		}
		return path, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return path, err
	}
	if err := m.ws.Write(planFile, "", true); err != nil {
		return path, err
	}
	return path, nil
}

func (m *PlanMode) readPlan() (string, string, error) {
	path, err := m.ws.Resolve(planFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || strings.Contains(err.Error(), "no such file") {
			return filepath.Join(m.ws.Root(), filepath.FromSlash(planFile)), "", nil
		}
		return "", "", err
	}
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return path, "", nil
		}
		return "", "", err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 1<<20+1))
	if err != nil {
		return "", "", err
	}
	if len(data) > 1<<20 {
		return "", "", errors.New("plan file exceeds 1 MiB")
	}
	return path, string(data), nil
}

func (m *PlanMode) saveLocked() error {
	if m.state == "" {
		return nil
	}
	data, err := json.Marshal(struct {
		Active bool `json:"active"`
	}{Active: m.active})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(m.state), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(m.state), ".plan-mode-*.tmp")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err := temporary.Chmod(0o600); err == nil {
		_, err = temporary.Write(data)
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return replaceStateFile(name, m.state)
}

type enterPlanModeTool struct{ mode *PlanMode }

func (t *enterPlanModeTool) Definition() api.ToolDefinition {
	return api.ToolDefinition{
		Type: "function", Name: "enter_plan_mode",
		Description: "Enter a read-only planning phase when the approach is ambiguous or the user asks for an implementation plan.",
		Parameters:  objectSchema(nil),
	}
}

func (t *enterPlanModeTool) Execute(ctx context.Context, _ json.RawMessage) (string, error) {
	if t.mode.Active() {
		return "Plan mode is already active.", nil
	}
	if t.mode.approver != nil {
		if err := t.mode.approver.Approve(ctx, "enter plan mode", "Explore the codebase and create an implementation plan"); err != nil {
			return "", err
		}
	}
	path, err := t.mode.seedPlan()
	if err != nil {
		return "", fmt.Errorf("seed plan file: %w", err)
	}
	if err := t.mode.SetActive(true); err != nil {
		return "", err
	}
	call, _ := ToolCallFromContext(ctx)
	event := PlanModeEvent{ToolCallID: call.ID, PlanPath: path}
	t.mode.mu.RLock()
	observer := t.mode.observer
	t.mode.mu.RUnlock()
	if observer != nil {
		observer.PlanModeEntered(event)
	}
	return encodePlanOutput(map[string]any{
		"status": "entered", "message": "You have entered plan mode. Explore the codebase and write the implementation plan to the plan file.",
		"planFilePath": path, "planFileSeed": "empty_or_existing",
	})
}

type exitPlanModeTool struct{ mode *PlanMode }

func (t *exitPlanModeTool) Definition() api.ToolDefinition {
	return api.ToolDefinition{
		Type: "function", Name: "exit_plan_mode", Description: "Exit plan mode and present the plan file for user approval.", Parameters: objectSchema(nil),
	}
}

func (t *exitPlanModeTool) Execute(ctx context.Context, _ json.RawMessage) (string, error) {
	if !t.mode.Active() {
		return "", errors.New("plan mode is not active")
	}
	path, content, err := t.mode.readPlan()
	if err != nil {
		return "", fmt.Errorf("read plan file: %w", err)
	}
	call, _ := ToolCallFromContext(ctx)
	event := PlanModeEvent{ToolCallID: call.ID, PlanPath: path, PlanContent: content}
	t.mode.mu.RLock()
	observer := t.mode.observer
	t.mode.mu.RUnlock()
	decision := PlanModeDecision{Outcome: "approved"}
	if observer != nil {
		decision, err = observer.ApprovePlanModeExit(ctx, event)
	} else if t.mode.approver != nil {
		err = t.mode.approver.Approve(ctx, "exit plan mode", content)
	}
	if err != nil {
		return "", err
	}
	if decision.Outcome == "cancelled" || decision.Outcome == "" {
		reason := strings.TrimSpace(decision.Feedback)
		if reason == "" {
			reason = "user requested plan revisions"
		}
		return "", &PermissionDeniedError{Action: "exit plan mode", Reason: reason}
	}
	if decision.Outcome != "approved" && decision.Outcome != "abandoned" {
		return "", fmt.Errorf("invalid plan approval outcome %q", decision.Outcome)
	}
	if err := t.mode.SetActive(false); err != nil {
		return "", err
	}
	if observer != nil {
		observer.PlanModeExited(event)
	}
	message := "Your plan has been approved. You can now start coding."
	if decision.Outcome == "abandoned" {
		message = "Plan mode was abandoned."
	} else if strings.TrimSpace(content) == "" {
		message = "Plan mode exit approved. No plan content was found -- you can proceed."
	}
	return encodePlanOutput(map[string]any{
		"status": decision.Outcome, "message": message, "planContent": content, "planFilePath": path,
	})
}

func encodePlanOutput(value any) (string, error) {
	data, err := json.Marshal(value)
	return string(data), err
}
