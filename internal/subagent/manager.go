package subagent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/agents"
	"github.com/lookcorner/go-cli/internal/hooks"
	"github.com/lookcorner/go-cli/internal/mcp"
	"github.com/lookcorner/go-cli/internal/skills"
	"github.com/lookcorner/go-cli/internal/tools"
	"github.com/lookcorner/go-cli/internal/workspace"
	"github.com/lookcorner/go-cli/internal/worktree"
)

type Observer interface {
	SubagentStarted(context.Context, Started)
	SubagentProgress(context.Context, tools.SubagentResult)
	SubagentEnded(context.Context, tools.SubagentResult)
}

type Started struct {
	ID             string
	Type           string
	Description    string
	Model          string
	CapabilityMode string
	ResumedFrom    string
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
	SkillConfig             skills.Config
	NewClient               func(ModelRuntime) (agent.ResponseStreamer, error)
	Observer                Observer
	Hooks                   *hooks.Catalog
	Worktrees               *worktree.Manager
	ProgressInterval        time.Duration
	ParentMCPServers        []mcp.ServerConfig
	StartMCPServers         func(context.Context, string, *tools.Registry, []mcp.ServerConfig) (func(), error)
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
	skillConfig             skills.Config
	newClient               func(ModelRuntime) (agent.ResponseStreamer, error)
	observer                Observer
	hooks                   *hooks.Catalog
	worktrees               *worktree.Manager
	progressInterval        time.Duration
	parentMCPServers        []mcp.ServerConfig
	startMCPServers         func(context.Context, string, *tools.Registry, []mcp.ServerConfig) (func(), error)
	tasks                   map[string]*task
}

type task struct {
	mu           sync.Mutex
	id           string
	typeName     string
	description  string
	started      time.Time
	cancel       context.CancelFunc
	done         chan struct{}
	once         sync.Once
	result       tools.SubagentResult
	runner       *agent.Runner
	hookRuntime  *hooks.Runtime
	cwd          string
	ownedTools   *tools.Registry
	worktreePath string
	snapshotRef  string
	responseID   string
	resumed      bool
	progress     agent.Progress
	model        string
	capability   string
	resumedFrom  string
	mcpResource  *mcpResource
}

type mcpResource struct {
	once  sync.Once
	close func()
}

func (r *mcpResource) Close() {
	if r != nil && r.close != nil {
		r.once.Do(r.close)
	}
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
		skills: config.Skills, skillConfig: config.SkillConfig,
		newClient: config.NewClient, observer: config.Observer, hooks: config.Hooks, worktrees: config.Worktrees,
		progressInterval: config.ProgressInterval, parentMCPServers: append([]mcp.ServerConfig(nil), config.ParentMCPServers...),
		startMCPServers: config.StartMCPServers, tasks: make(map[string]*task),
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
	isolation := first(request.Isolation, definition.Isolation)
	if isolation != "" && isolation != "none" && isolation != "worktree" {
		return tools.SubagentResult{}, fmt.Errorf("invalid subagent isolation %q", isolation)
	}
	background := request.Background
	if !request.BackgroundSet && definition.Background != nil {
		background = *definition.Background
	}
	if request.ResumeFrom != "" {
		return m.resume(ctx, request, definition, background)
	}
	id := newID()
	childRoot, childRegistry, worktreePath, err := m.prepareWorkspace(ctx, id, request.CWD, isolation)
	if err != nil {
		return tools.SubagentResult{}, err
	}
	keepRegistry := false
	if childRegistry != nil {
		defer func() {
			if !keepRegistry {
				_ = childRegistry.Close()
				m.removeWorktree(worktreePath)
			}
		}()
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
	toolSource := m.tools
	if childRegistry != nil {
		toolSource = childRegistry
	}
	view := toolSource.View(definition.Tools, definition.DisallowedTools, capability)
	if definition.Plugin != "" {
		view.FilterMCPServers(func(string) bool { return false })
	} else {
		view.FilterMCPServers(definition.MCPInheritance.Allows)
	}
	ownedMCP, err := m.ownedMCPServers(definition)
	if err != nil {
		return tools.SubagentResult{}, err
	}
	var ownedMCPResource *mcpResource
	if len(ownedMCP) > 0 {
		ownedNames := make(map[string]bool, len(ownedMCP))
		for _, server := range ownedMCP {
			ownedNames[server.Name] = true
		}
		view.FilterMCPServers(func(name string) bool { return !ownedNames[name] })
		if m.startMCPServers == nil {
			return tools.SubagentResult{}, errors.New("subagent-owned MCP servers are not available")
		}
		closeMCP, err := m.startMCPServers(m.ctx, childRoot, view, ownedMCP)
		if err != nil {
			return tools.SubagentResult{}, err
		}
		ownedMCPResource = &mcpResource{close: closeMCP}
		defer func() {
			if !keepRegistry {
				ownedMCPResource.Close()
			}
		}()
	}
	childSkills, err := m.childSkills(definition, childRoot)
	if err != nil {
		return tools.SubagentResult{}, err
	}
	instructions := definition.Prompt
	if childSkills != nil {
		instructions = childSkills.Preload(definition.Skills) + instructions
	}
	if view.HasTool("skill") {
		var replacement []tools.Tool
		if childSkills != nil {
			replacement = []tools.Tool{childSkills.Tool()}
		}
		if _, err := view.Replace([]string{"skill"}, replacement); err != nil {
			return tools.SubagentResult{}, err
		}
	}
	runner := &agent.Runner{
		Client: client, Tools: view, Model: model.Model, ReasoningEffort: effort, Instructions: instructions,
		SessionID: id, MaxSteps: definition.MaxTurns, ContextWindow: model.ContextWindow,
		CompactThresholdPercent: model.CompactThresholdPercent, Skills: childSkills,
	}
	var hookRuntime *hooks.Runtime
	if catalog := m.childHooks(definition, childRoot); catalog != nil {
		hookRuntime = &hooks.Runtime{Catalog: catalog, WorkspaceRoot: childRoot, SessionID: id, Model: model.Model, SubagentType: request.Type}
		runner.HookPolicy = hookRuntime
	}
	current := &task{id: id, typeName: request.Type, description: request.Description, started: time.Now(), done: make(chan struct{}), runner: runner, hookRuntime: hookRuntime, cwd: childRoot, ownedTools: childRegistry, worktreePath: worktreePath, model: model.Model, capability: capability, mcpResource: ownedMCPResource}
	runner.Progress = current.updateProgress
	m.mu.Lock()
	m.tasks[id] = current
	m.mu.Unlock()
	keepRegistry = true
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
	if err := m.rehydrateWorktree(ctx, previous, id); err != nil {
		previous.mu.Lock()
		previous.resumed = false
		previous.mu.Unlock()
		return tools.SubagentResult{}, err
	}
	var hookRuntime *hooks.Runtime
	if previous.hookRuntime != nil {
		hookRuntime = &hooks.Runtime{Catalog: previous.hookRuntime.Catalog, WorkspaceRoot: previous.cwd, SessionID: id, Model: previous.runner.Model, SubagentType: request.Type}
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
	current := &task{id: id, typeName: request.Type, description: request.Description, started: time.Now(), done: make(chan struct{}), runner: runner, responseID: previous.responseID, hookRuntime: hookRuntime, cwd: previous.cwd, ownedTools: previous.ownedTools, worktreePath: previous.worktreePath, model: runner.Model, capability: previous.capability, resumedFrom: previous.id, mcpResource: previous.mcpResource}
	runner.Progress = current.updateProgress
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
		m.observer.SubagentStarted(runCtx, Started{
			ID: current.id, Type: current.typeName, Description: current.description,
			Model: current.model, CapabilityMode: current.capability, ResumedFrom: current.resumedFrom,
		})
	}
	if current.hookRuntime != nil {
		current.hookRuntime.SubagentStarted(runCtx, current.id, current.typeName, current.description)
	}
	stopProgress := m.publishProgress(runCtx, current)
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
		if current.hookRuntime != nil {
			current.hookRuntime.SubagentEnded(context.Background(), current.id, current.typeName, status, duration)
		}
		worktreeDir := m.disposeWorktree(current, status)
		final := tools.SubagentResult{
			ID: current.id, Type: current.typeName, Status: status, Output: output,
			ToolCalls: result.ToolCalls, Turns: result.Steps, TokensUsed: result.TokensUsed,
			ContextWindow: current.runner.ContextWindow, ContextUsage: contextUsage(result.InputTokens, current.runner.ContextWindow),
			ToolsUsed: append([]string{}, result.ToolsUsed...), ErrorCount: result.ErrorCount,
			DurationMS: duration, WorktreeDir: worktreeDir, Description: current.description, StartedAtMS: current.started.UnixMilli(),
		}
		stopProgress()
		if m.observer != nil {
			m.observer.SubagentEnded(context.Background(), final)
		}
		current.finish(final)
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

func (m *Manager) publishProgress(ctx context.Context, current *task) func() {
	if m.observer == nil {
		return func() {}
	}
	interval := m.progressInterval
	if interval <= 0 {
		interval = 2 * time.Second
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		var last agent.Progress
		lastEmit := time.Now()
		for {
			select {
			case <-stop:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				current.mu.Lock()
				progress := current.progress
				progress.ToolsUsed = append([]string(nil), progress.ToolsUsed...)
				current.mu.Unlock()
				changed := progress.Turns != last.Turns || progress.ToolCalls != last.ToolCalls || progress.InputTokens != last.InputTokens || progress.ErrorCount != last.ErrorCount || progress.TokensUsed != last.TokensUsed
				if !changed && time.Since(lastEmit) < 4*interval {
					continue
				}
				last, lastEmit = progress, time.Now()
				m.observer.SubagentProgress(ctx, current.runningResult())
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() { close(stop) })
		<-done
	}
}

func (t *task) finish(result tools.SubagentResult) {
	t.once.Do(func() {
		t.result = result
		close(t.done)
	})
}

func (t *task) runningResult() tools.SubagentResult {
	t.mu.Lock()
	progress := t.progress
	t.mu.Unlock()
	return tools.SubagentResult{
		ID: t.id, Type: t.typeName, Status: "running", WorktreeDir: t.worktreePath,
		Description: t.description, StartedAtMS: t.started.UnixMilli(), DurationMS: time.Since(t.started).Milliseconds(),
		Turns: progress.Turns, ToolCalls: progress.ToolCalls, TokensUsed: progress.InputTokens,
		ContextWindow: t.runner.ContextWindow, ContextUsage: contextUsage(progress.InputTokens, t.runner.ContextWindow),
		ToolsUsed: append([]string{}, progress.ToolsUsed...), ErrorCount: progress.ErrorCount,
	}
}

func (t *task) updateProgress(progress agent.Progress) {
	t.mu.Lock()
	t.progress = progress
	t.mu.Unlock()
}

func contextUsage(tokens, window int) int {
	if tokens <= 0 || window <= 0 {
		return 0
	}
	return min(100, tokens*100/window)
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
	closed := make(map[*tools.Registry]bool)
	closedMCP := make(map[*mcpResource]bool)
	for _, current := range tasks {
		if current.ownedTools != nil && !closed[current.ownedTools] {
			closed[current.ownedTools] = true
			_ = current.ownedTools.Close()
		}
		if current.mcpResource != nil && !closedMCP[current.mcpResource] {
			closedMCP[current.mcpResource] = true
			current.mcpResource.Close()
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

func (m *Manager) ownedMCPServers(definition agents.Definition) ([]mcp.ServerConfig, error) {
	if definition.Plugin != "" || len(definition.MCPServers) == 0 {
		return nil, nil
	}
	parent := make(map[string]mcp.ServerConfig, len(m.parentMCPServers))
	for _, server := range m.parentMCPServers {
		parent[server.Name] = server
	}
	servers := make([]mcp.ServerConfig, 0, len(definition.MCPServers))
	for _, ref := range definition.MCPServers {
		if ref.Config == nil {
			if server, ok := parent[ref.Name]; ok {
				servers = append(servers, server)
			}
			continue
		}
		server, err := decodeMCPServer(ref)
		if err != nil {
			return nil, fmt.Errorf("agent %q mcpServers %q: %w", definition.Name, ref.Name, err)
		}
		servers = append(servers, server)
	}
	return servers, nil
}

func decodeMCPServer(ref agents.MCPServerRef) (mcp.ServerConfig, error) {
	data, err := json.Marshal(ref.Config)
	if err != nil {
		return mcp.ServerConfig{}, err
	}
	var raw struct {
		Type    string          `json:"type"`
		Command string          `json:"command"`
		Args    []string        `json:"args"`
		Env     json.RawMessage `json:"env"`
		URL     string          `json:"url"`
		Headers json.RawMessage `json:"headers"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return mcp.ServerConfig{}, err
	}
	env, err := decodeNameValues(raw.Env)
	if err != nil {
		return mcp.ServerConfig{}, fmt.Errorf("env: %w", err)
	}
	headers, err := decodeNameValues(raw.Headers)
	if err != nil {
		return mcp.ServerConfig{}, fmt.Errorf("headers: %w", err)
	}
	if strings.TrimSpace(raw.Command) == "" && strings.TrimSpace(raw.URL) == "" {
		return mcp.ServerConfig{}, errors.New("command or url is required")
	}
	return mcp.ServerConfig{
		Name: ref.Name, Type: raw.Type, Command: raw.Command, Args: raw.Args,
		Env: env, URL: raw.URL, Headers: headers,
	}, nil
}

func decodeNameValues(raw json.RawMessage) (map[string]string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var values map[string]string
	if err := json.Unmarshal(raw, &values); err == nil {
		return values, nil
	}
	var pairs []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	}
	if err := json.Unmarshal(raw, &pairs); err != nil {
		return nil, errors.New("must be a string map or name/value list")
	}
	values = make(map[string]string, len(pairs))
	for _, pair := range pairs {
		if pair.Name == "" {
			return nil, errors.New("name must not be empty")
		}
		values[pair.Name] = pair.Value
	}
	return values, nil
}

func (m *Manager) childSkills(definition agents.Definition, root string) (*skills.Catalog, error) {
	if !definition.DiscoverSkills {
		return nil, nil
	}
	if definition.InheritSkills && m.skills != nil {
		return m.skills.Clone(), nil
	}
	config := m.skillConfig
	if !definition.InheritSkills {
		config.Paths = nil
		config.Ignore = nil
		config.Disabled = nil
	}
	return skills.Discover(root, config)
}

func (m *Manager) childHooks(definition agents.Definition, root string) *hooks.Catalog {
	if len(definition.Hooks) == 0 || definition.Plugin != "" || definition.Scope == "project" && (m.hooks == nil || !m.hooks.ProjectTrusted()) {
		return m.hooks
	}
	return m.hooks.WithInline(definition.Hooks, root, "agent/"+definition.Name+"/", "agent "+definition.Name)
}

func (m *Manager) childWorkspace(raw string) (string, *tools.Registry, error) {
	path := sanitizeCWD(raw)
	if path == "" {
		return m.workspaceRoot, nil, nil
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(m.workspaceRoot, path)
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil, fmt.Errorf("subagent cwd %q does not exist", path)
		}
		return "", nil, fmt.Errorf("stat subagent cwd %q: %w", path, err)
	}
	if !info.IsDir() {
		return "", nil, fmt.Errorf("subagent cwd %q is not a directory", path)
	}
	ws, err := workspace.Open(path)
	if err != nil {
		return "", nil, err
	}
	if canonicalPath(ws.Root()) == canonicalPath(m.workspaceRoot) {
		return m.workspaceRoot, nil, nil
	}
	return ws.Root(), m.tools.ForWorkspace(ws), nil
}

func (m *Manager) prepareWorkspace(ctx context.Context, id, rawCWD, isolation string) (string, *tools.Registry, string, error) {
	if isolation != "worktree" || m.worktrees == nil {
		root, registry, err := m.childWorkspace(rawCWD)
		return root, registry, "", err
	}
	record, _, err := m.worktrees.Create(ctx, worktree.CreateRequest{
		SessionID: id, SourcePath: m.workspaceRoot, CopyMode: "dirty", WorktreeType: "linked", Label: "subagent-" + id,
	})
	if err != nil {
		root, registry, sharedErr := m.childWorkspace(rawCWD)
		return root, registry, "", sharedErr
	}
	effective, err := worktree.EffectiveCWD(ctx, m.workspaceRoot, record.Path)
	if err != nil {
		m.removeWorktree(record.Path)
		root, registry, sharedErr := m.childWorkspace(rawCWD)
		return root, registry, "", sharedErr
	}
	root, registry, err := m.childWorkspace(effective)
	if err != nil {
		m.removeWorktree(record.Path)
		return "", nil, "", err
	}
	return root, registry, record.Path, nil
}

func (m *Manager) disposeWorktree(current *task, status string) string {
	if current.worktreePath == "" || m.worktrees == nil {
		return current.worktreePath
	}
	ref := "refs/gork/subagents/" + current.id
	if _, err := worktree.SnapshotToRef(context.Background(), current.worktreePath, ref, "subagent "+current.id+" "+status); err != nil {
		return current.worktreePath
	}
	current.snapshotRef = ref
	removed, _, err := m.worktrees.Remove(context.Background(), worktree.RemoveRequest{WorktreePath: current.worktreePath, Force: true})
	if err != nil || !removed {
		return current.worktreePath
	}
	return ""
}

func (m *Manager) rehydrateWorktree(ctx context.Context, previous *task, id string) error {
	if previous.worktreePath == "" {
		return nil
	}
	if info, err := os.Stat(previous.worktreePath); err == nil && info.IsDir() {
		return nil
	}
	if m.worktrees == nil || previous.snapshotRef == "" {
		return errors.New("subagent worktree is unavailable for resume")
	}
	if _, err := m.worktrees.Rehydrate(ctx, worktree.RehydrateRequest{
		SessionID: id, SourceRepo: m.workspaceRoot, WorktreePath: previous.worktreePath,
		SnapshotRef: previous.snapshotRef, Label: "subagent-" + id,
	}); err != nil {
		return fmt.Errorf("rehydrate subagent worktree: %w", err)
	}
	_ = worktree.DeleteSnapshotRef(ctx, m.workspaceRoot, previous.snapshotRef)
	return nil
}

func (m *Manager) removeWorktree(path string) {
	if path != "" && m.worktrees != nil {
		_, _, _ = m.worktrees.Remove(context.Background(), worktree.RemoveRequest{WorktreePath: path, Force: true})
	}
}

func sanitizeCWD(value string) string {
	value = strings.TrimSpace(strings.Trim(strings.TrimSpace(value), "\"'`"))
	switch strings.ToLower(value) {
	case "", "null", "none", "undefined":
		return ""
	}
	if value == "~" || strings.HasPrefix(value, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			value = filepath.Join(home, strings.TrimPrefix(value, "~/"))
		}
	}
	return value
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
