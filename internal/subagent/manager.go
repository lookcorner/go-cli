package subagent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/agents"
	"github.com/lookcorner/go-cli/internal/hooks"
	"github.com/lookcorner/go-cli/internal/skills"
	"github.com/lookcorner/go-cli/internal/tools"
)

type Observer interface {
	SubagentStarted(context.Context, string, string, string)
	SubagentEnded(context.Context, string, string, string, int64)
}

type ModelRuntime struct {
	Profile                 string
	Model                   string
	ContextWindow           int
	CompactThresholdPercent int
}

type Config struct {
	Context                 context.Context
	Catalog                 *agents.Catalog
	Tools                   *tools.Registry
	WorkspaceRoot           string
	ParentModel             string
	ContextWindow           int
	CompactThresholdPercent int
	ResolveModel            func(string) (ModelRuntime, bool)
	AvailableModels         []string
	Skills                  *skills.Catalog
	NewClient               func(ModelRuntime) (agent.ResponseStreamer, error)
	Observer                Observer
	Hooks                   *hooks.Catalog
}

type Manager struct {
	ctx                     context.Context
	cancel                  context.CancelFunc
	mu                      sync.RWMutex
	catalog                 *agents.Catalog
	tools                   *tools.Registry
	workspaceRoot           string
	parentModel             string
	contextWindow           int
	compactThresholdPercent int
	resolveModel            func(string) (ModelRuntime, bool)
	availableModels         []string
	skills                  *skills.Catalog
	newClient               func(ModelRuntime) (agent.ResponseStreamer, error)
	observer                Observer
	hooks                   *hooks.Catalog
	tasks                   map[string]*task
}

type task struct {
	mu          sync.Mutex
	id          string
	typeName    string
	description string
	started     time.Time
	cancel      context.CancelFunc
	done        chan struct{}
	once        sync.Once
	result      tools.SubagentResult
	runner      *agent.Runner
	hookRuntime *hooks.Runtime
	responseID  string
	resumed     bool
}

func New(config Config) (*Manager, error) {
	if config.Catalog == nil || config.Tools == nil || config.NewClient == nil {
		return nil, errors.New("subagent catalog, tools, and client factory are required")
	}
	ctx := config.Context
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithCancel(ctx)
	return &Manager{
		ctx: ctx, cancel: cancel, catalog: config.Catalog, tools: config.Tools,
		workspaceRoot: config.WorkspaceRoot, parentModel: config.ParentModel,
		contextWindow: config.ContextWindow, compactThresholdPercent: config.CompactThresholdPercent,
		resolveModel: config.ResolveModel, availableModels: append([]string(nil), config.AvailableModels...),
		skills:    config.Skills,
		newClient: config.NewClient, observer: config.Observer, hooks: config.Hooks, tasks: make(map[string]*task),
	}, nil
}

func (m *Manager) SetCatalog(catalog *agents.Catalog) {
	if catalog == nil {
		return
	}
	m.mu.Lock()
	m.catalog = catalog
	m.mu.Unlock()
}

func (m *Manager) Description() string {
	m.mu.RLock()
	definitions := m.catalog.Definitions()
	m.mu.RUnlock()
	lines := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		lines = append(lines, fmt.Sprintf("- %s: %s", definition.Name, definition.Description))
	}
	sort.Strings(lines)
	return "Launch a subagent for an independent task. Available subagent types:\n" + strings.Join(lines, "\n") + "\nBackground execution is the default; use get_task_output to retrieve results."
}

func (m *Manager) Start(ctx context.Context, request tools.SubagentRequest) (tools.SubagentResult, error) {
	m.mu.RLock()
	definition, ok := m.catalog.ByName(request.Type)
	m.mu.RUnlock()
	if !ok {
		return tools.SubagentResult{}, fmt.Errorf("unknown subagent type %q", request.Type)
	}
	if definition.PermissionMode == "bypassPermissions" {
		return tools.SubagentResult{}, errors.New("subagent permissionMode bypassPermissions is not enabled")
	}
	effort := first(request.ReasoningEffort, definition.Effort)
	if !validEffort(effort) {
		return tools.SubagentResult{}, fmt.Errorf("invalid subagent reasoning_effort %q", effort)
	}
	if request.CWD != "" && canonicalPath(request.CWD) != canonicalPath(m.workspaceRoot) {
		return tools.SubagentResult{}, errors.New("custom subagent cwd is not supported by this workspace session")
	}
	isolation := first(request.Isolation, definition.Isolation)
	if isolation != "" && isolation != "none" {
		return tools.SubagentResult{}, errors.New("subagent worktree isolation is not implemented")
	}
	background := request.Background
	if !request.BackgroundSet && definition.Background != nil {
		background = *definition.Background
	}
	if request.ResumeFrom != "" {
		return m.resume(ctx, request, definition, background)
	}
	model := ModelRuntime{Model: m.parentModel, ContextWindow: m.contextWindow, CompactThresholdPercent: m.compactThresholdPercent}
	if request.Model != "" {
		resolved, ok := m.resolve(request.Model)
		if !ok {
			valid := strings.Join(m.availableModels, ", ")
			if valid == "" {
				return tools.SubagentResult{}, fmt.Errorf("unknown Task.model slug %q; no valid model slugs are currently available; omit model to inherit the parent model", request.Model)
			}
			return tools.SubagentResult{}, fmt.Errorf("unknown Task.model slug %q; valid model slugs: %s; omit model to inherit the parent model", request.Model, valid)
		}
		model = resolved
	} else if definition.Model != "" {
		if resolved, ok := m.resolve(definition.Model); ok {
			model = resolved
		} else if m.resolveModel == nil {
			model.Model = definition.Model
		}
	}
	client, err := m.newClient(model)
	if err != nil {
		return tools.SubagentResult{}, err
	}
	capability := request.CapabilityMode
	if capability == "" && (request.Type == "explore" || request.Type == "plan") {
		capability = "read-only"
	}
	view := m.tools.View(definition.Tools, definition.DisallowedTools, capability)
	childSkills := m.skills.Clone()
	if childSkills != nil && view.HasTool("skill") {
		if _, err := view.Replace([]string{"skill"}, []tools.Tool{childSkills.Tool()}); err != nil {
			return tools.SubagentResult{}, err
		}
	}
	id := newID()
	runner := &agent.Runner{
		Client: client, Tools: view, Model: model.Model, ReasoningEffort: effort, Instructions: definition.Prompt,
		SessionID: id, MaxSteps: definition.MaxTurns, ContextWindow: model.ContextWindow,
		CompactThresholdPercent: model.CompactThresholdPercent, Skills: childSkills,
	}
	var hookRuntime *hooks.Runtime
	if m.hooks != nil {
		hookRuntime = &hooks.Runtime{Catalog: m.hooks, WorkspaceRoot: m.workspaceRoot, SessionID: id, Model: model.Model, SubagentType: request.Type}
		runner.HookPolicy = hookRuntime
	}
	current := &task{id: id, typeName: request.Type, description: request.Description, started: time.Now(), done: make(chan struct{}), runner: runner, hookRuntime: hookRuntime}
	m.mu.Lock()
	m.tasks[id] = current
	m.mu.Unlock()
	return m.launch(ctx, current, request.Prompt, background)
}

func (m *Manager) resume(ctx context.Context, request tools.SubagentRequest, definition agents.Definition, background bool) (tools.SubagentResult, error) {
	m.mu.RLock()
	previous := m.tasks[request.ResumeFrom]
	m.mu.RUnlock()
	if previous == nil {
		return tools.SubagentResult{}, fmt.Errorf("unknown resume_from subagent %q", request.ResumeFrom)
	}
	select {
	case <-previous.done:
	default:
		return tools.SubagentResult{}, errors.New("resume_from subagent is still running")
	}
	if previous.typeName != request.Type {
		return tools.SubagentResult{}, errors.New("resume_from subagent type must match subagent_type")
	}
	previous.mu.Lock()
	if previous.resumed {
		previous.mu.Unlock()
		return tools.SubagentResult{}, errors.New("resume_from subagent has already been resumed")
	}
	previous.resumed = true
	previous.mu.Unlock()
	id := newID()
	var hookRuntime *hooks.Runtime
	if m.hooks != nil {
		hookRuntime = &hooks.Runtime{Catalog: m.hooks, WorkspaceRoot: m.workspaceRoot, SessionID: id, Model: previous.runner.Model, SubagentType: request.Type}
	}
	runner := &agent.Runner{
		Client: previous.runner.Client, Tools: previous.runner.Tools, Model: previous.runner.Model,
		ReasoningEffort: first(request.ReasoningEffort, definition.Effort, previous.runner.ReasoningEffort),
		Instructions:    previous.runner.Instructions, SessionID: id, MaxSteps: previous.runner.MaxSteps,
		ContextWindow: previous.runner.ContextWindow, CompactThresholdPercent: previous.runner.CompactThresholdPercent,
		Skills: previous.runner.Skills,
	}
	if hookRuntime != nil {
		runner.HookPolicy = hookRuntime
	}
	current := &task{id: id, typeName: request.Type, description: request.Description, started: time.Now(), done: make(chan struct{}), runner: runner, responseID: previous.responseID, hookRuntime: hookRuntime}
	m.mu.Lock()
	m.tasks[id] = current
	m.mu.Unlock()
	return m.launch(ctx, current, request.Prompt, background)
}

func (m *Manager) launch(caller context.Context, current *task, prompt string, background bool) (tools.SubagentResult, error) {
	base := caller
	if background {
		base = m.ctx
	}
	runCtx, cancel := context.WithCancel(base)
	current.mu.Lock()
	current.cancel = cancel
	current.mu.Unlock()
	if m.observer != nil {
		m.observer.SubagentStarted(runCtx, current.id, current.typeName, current.description)
	}
	run := func() {
		result, err := current.runner.RunTurn(runCtx, prompt, current.responseID)
		status, output := "completed", result.Text
		if err != nil {
			status, output = "failed", err.Error()
			if errors.Is(runCtx.Err(), context.Canceled) {
				status = "cancelled"
			}
		}
		if current.hookRuntime != nil {
			current.hookRuntime.SessionEnded(context.Background(), status)
		}
		current.responseID = result.ResponseID
		duration := time.Since(current.started).Milliseconds()
		current.finish(tools.SubagentResult{ID: current.id, Type: current.typeName, Status: status, Output: output, ToolCalls: result.ToolCalls, Turns: result.Steps, DurationMS: duration, Description: current.description, StartedAtMS: current.started.UnixMilli(), ContextWindow: current.runner.ContextWindow})
		if m.observer != nil {
			m.observer.SubagentEnded(context.Background(), current.id, current.typeName, status, duration)
		}
	}
	if background {
		go run()
		return current.runningResult(), nil
	}
	run()
	if current.result.Status == "failed" {
		return current.result, errors.New(current.result.Output)
	}
	return current.result, nil
}

func (t *task) finish(result tools.SubagentResult) {
	t.once.Do(func() {
		t.result = result
		close(t.done)
	})
}

func (t *task) runningResult() tools.SubagentResult {
	return tools.SubagentResult{ID: t.id, Type: t.typeName, Status: "running", Description: t.description, StartedAtMS: t.started.UnixMilli(), DurationMS: time.Since(t.started).Milliseconds(), ContextWindow: t.runner.ContextWindow}
}

func (m *Manager) Has(id string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.tasks[id] != nil
}

func (m *Manager) Output(ctx context.Context, id string, timeout time.Duration) (tools.SubagentResult, error) {
	m.mu.RLock()
	current := m.tasks[id]
	m.mu.RUnlock()
	if current == nil {
		return tools.SubagentResult{}, fmt.Errorf("unknown subagent %q", id)
	}
	if timeout <= 0 {
		select {
		case <-current.done:
			return current.result, nil
		default:
			return current.runningResult(), nil
		}
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-current.done:
		return current.result, nil
	case <-timer.C:
		return current.runningResult(), nil
	case <-ctx.Done():
		return tools.SubagentResult{}, ctx.Err()
	}
}

func (m *Manager) List() []tools.SubagentResult {
	m.mu.RLock()
	tasks := make([]*task, 0, len(m.tasks))
	for _, current := range m.tasks {
		tasks = append(tasks, current)
	}
	m.mu.RUnlock()
	results := make([]tools.SubagentResult, 0, len(tasks))
	for _, current := range tasks {
		select {
		case <-current.done:
			results = append(results, current.result)
		default:
			results = append(results, current.runningResult())
		}
	}
	sort.Slice(results, func(i, j int) bool { return results[i].StartedAtMS < results[j].StartedAtMS })
	return results
}

func (m *Manager) Kill(ctx context.Context, id string) (string, error) {
	m.mu.RLock()
	current := m.tasks[id]
	m.mu.RUnlock()
	if current == nil {
		return "not_found", fmt.Errorf("unknown subagent %q", id)
	}
	select {
	case <-current.done:
		return "already_finished", nil
	default:
		current.mu.Lock()
		cancel := current.cancel
		current.mu.Unlock()
		if cancel != nil {
			cancel()
		}
	}
	select {
	case <-current.done:
		return "killed", nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (m *Manager) Close() {
	m.cancel()
	m.mu.RLock()
	tasks := make([]*task, 0, len(m.tasks))
	for _, current := range m.tasks {
		tasks = append(tasks, current)
	}
	m.mu.RUnlock()
	for _, current := range tasks {
		current.mu.Lock()
		cancel := current.cancel
		current.mu.Unlock()
		if cancel != nil {
			cancel()
		}
	}
	waitCtx, cancelWait := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelWait()
	for _, current := range tasks {
		select {
		case <-current.done:
		case <-waitCtx.Done():
			return
		}
	}
}

func newID() string {
	var value [8]byte
	_, _ = rand.Read(value[:])
	return "subagent_" + hex.EncodeToString(value[:])
}

func first(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func validEffort(value string) bool {
	switch value {
	case "", "low", "medium", "high", "xhigh", "max":
		return true
	default:
		return false
	}
}

func (m *Manager) resolve(model string) (ModelRuntime, bool) {
	if m.resolveModel == nil {
		return ModelRuntime{Model: model, ContextWindow: m.contextWindow, CompactThresholdPercent: m.compactThresholdPercent}, true
	}
	return m.resolveModel(model)
}

func canonicalPath(path string) string {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	if real, err := filepath.EvalSymlinks(absolute); err == nil {
		return real
	}
	return filepath.Clean(absolute)
}
