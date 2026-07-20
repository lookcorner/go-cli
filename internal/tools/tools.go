package tools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/lookcorner/go-cli/internal/api"
	"github.com/lookcorner/go-cli/internal/workspace"
)

const (
	maxReadBytes   = 2 << 20
	maxPDFBytes    = 50 << 20
	maxPPTXBytes   = 50 << 20
	maxWriteBytes  = 4 << 20
	maxOutputBytes = 256 << 10
)

type PermissionMode string

const (
	PermissionPrompt PermissionMode = "prompt"
	PermissionAuto   PermissionMode = "auto"
	PermissionDeny   PermissionMode = "deny"
)

type Approver interface {
	Approve(ctx context.Context, action, detail string) error
}

type PromptApprover struct {
	Mode   PermissionMode
	Input  io.Reader
	Output io.Writer
}

func (a PromptApprover) Approve(_ context.Context, action, detail string) error {
	switch a.Mode {
	case PermissionAuto:
		return nil
	case PermissionDeny:
		return &PermissionDeniedError{Action: action}
	case PermissionPrompt:
		if a.Input == nil || a.Output == nil {
			return &PermissionDeniedError{Action: action, Reason: fmt.Sprintf("permission prompt unavailable for %s", action)}
		}
		fmt.Fprintf(a.Output, "\nAllow %s?\n  %s\n[y/N] ", action, detail)
		var line string
		var err error
		if reader, ok := a.Input.(interface{ ReadString(byte) (string, error) }); ok {
			line, err = reader.ReadString('\n')
		} else {
			line, err = bufio.NewReader(a.Input).ReadString('\n')
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return fmt.Errorf("read approval: %w", err)
		}
		answer := strings.ToLower(strings.TrimSpace(line))
		if answer == "y" || answer == "yes" {
			return nil
		}
		return &PermissionDeniedError{Action: action}
	default:
		return fmt.Errorf("unknown permission mode %q", a.Mode)
	}
}

type Tool interface {
	Definition() api.ToolDefinition
	Execute(context.Context, json.RawMessage) (string, error)
}

type ResultTool interface {
	Tool
	ExecuteResult(context.Context, json.RawMessage) (ExecutionResult, error)
}

type ExecutionResult struct {
	Output string
	Images []ImageAttachment
}

type ImageAttachment struct {
	MediaType string
	Data      []byte
	Width     int
	Height    int
}

type ToolCallContext struct {
	ID   string
	Name string
}

type toolCallContextKey struct{}

func WithToolCall(ctx context.Context, id, name string) context.Context {
	return context.WithValue(ctx, toolCallContextKey{}, ToolCallContext{ID: id, Name: name})
}

func ToolCallFromContext(ctx context.Context) (ToolCallContext, bool) {
	value, ok := ctx.Value(toolCallContextKey{}).(ToolCallContext)
	return value, ok
}

type Registry struct {
	mu            sync.RWMutex
	tools         map[string]Tool
	approver      Approver
	processes     *ProcessManager
	goal          *GoalStore
	scheduler     *Scheduler
	ownsScheduler bool
	plan          *PlanMode
	questions     *UserQuestions
	readPolicy    Approver
	hunks         *HunkTracker
	rewind        *mutationCheckpoint
	readFile      *readFileTool
	webFetch      *webFetchTool
	subagents     *subagentHolder
}

type mutationCheckpoint struct {
	mu          sync.RWMutex
	store       *workspace.RewindStore
	promptIndex func() int
}

type workspaceMutation struct {
	store      *workspace.RewindStore
	checkpoint *workspace.WorkspaceCheckpoint
}

func (c *mutationCheckpoint) before(path string) error {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	store, promptIndex := c.store, c.promptIndex
	c.mu.RUnlock()
	if store == nil || promptIndex == nil {
		return nil
	}
	index := promptIndex()
	if index < 0 {
		return errors.New("file mutation has no active prompt checkpoint")
	}
	return store.CaptureBefore(index, path)
}

func (c *mutationCheckpoint) after(path string) error {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	store, promptIndex := c.store, c.promptIndex
	c.mu.RUnlock()
	if store == nil || promptIndex == nil {
		return nil
	}
	return store.CaptureAfter(promptIndex(), path)
}

func (c *mutationCheckpoint) cancel(path string) {
	if c == nil {
		return
	}
	c.mu.RLock()
	store, promptIndex := c.store, c.promptIndex
	c.mu.RUnlock()
	if store != nil && promptIndex != nil {
		_ = store.Cancel(promptIndex(), path)
	}
}

func (c *mutationCheckpoint) beforeWorkspace() (*workspaceMutation, error) {
	if c == nil {
		return nil, nil
	}
	c.mu.RLock()
	store, promptIndex := c.store, c.promptIndex
	c.mu.RUnlock()
	if store == nil || promptIndex == nil {
		return nil, nil
	}
	checkpoint, err := store.CaptureWorkspaceBefore(promptIndex())
	if err != nil {
		return nil, err
	}
	return &workspaceMutation{store: store, checkpoint: checkpoint}, nil
}

func (c *mutationCheckpoint) afterWorkspace(mutation *workspaceMutation) error {
	if mutation == nil {
		return nil
	}
	return mutation.store.CaptureWorkspaceAfter(mutation.checkpoint)
}

func NewRegistry(ws *workspace.Workspace, approver Approver) *Registry {
	processes := NewProcessManager(ws, approver)
	subagents := &subagentHolder{}
	todos := newTodoStore()
	goal := NewGoalStore()
	scheduler := NewScheduler()
	plan := NewPlanMode(ws, approver)
	questions := &UserQuestions{plan: plan, timeoutEnabled: true, timeout: 30 * time.Minute}
	rewind := &mutationCheckpoint{}
	processes.rewind = rewind
	readFile := &readFileTool{ws: ws}
	webFetch := &webFetchTool{
		approver: approver, restrictDomains: true,
		domainRules: buildWebDomainRules(defaultWebAllowedDomains),
	}
	items := []Tool{
		readFile,
		&listFilesTool{ws: ws},
		&searchFilesTool{ws: ws},
		&writeFileTool{ws: ws, approver: approver, rewind: rewind},
		&editFileTool{ws: ws, approver: approver, rewind: rewind},
		&shellTool{ws: ws, approver: approver, timeout: 2 * time.Minute, rewind: rewind},
		&startCommandTool{manager: processes},
		&commandOutputTool{manager: processes},
		&killCommandTool{manager: processes},
		&runTerminalCommandTool{manager: processes},
		&monitorTool{manager: processes},
		&taskOutputTool{manager: processes, subagents: subagents},
		&killTaskTool{manager: processes, subagents: subagents},
		&listDirTool{ws: ws},
		&grepTool{ws: ws},
		&searchReplaceTool{ws: ws, approver: approver, rewind: rewind},
		&todoWriteTool{store: todos},
		&updateGoalTool{store: goal},
		&schedulerCreateTool{scheduler: scheduler},
		&schedulerListTool{scheduler: scheduler},
		&schedulerDeleteTool{scheduler: scheduler},
		&enterPlanModeTool{mode: plan},
		&exitPlanModeTool{mode: plan},
		&askUserQuestionTool{questions: questions},
		webFetch,
	}
	registry := &Registry{
		tools: make(map[string]Tool, len(items)), approver: approver, processes: processes, goal: goal,
		hunks: NewHunkTracker(ws), rewind: rewind, readFile: readFile, webFetch: webFetch,
		subagents: subagents, scheduler: scheduler, ownsScheduler: true, plan: plan, questions: questions,
	}
	for _, item := range items {
		registry.tools[item.Definition().Name] = item
	}
	return registry
}

// ForWorkspace rebuilds workspace-bound tools while sharing external adapters.
func (r *Registry) ForWorkspace(ws *workspace.Workspace) *Registry {
	if r == nil {
		return nil
	}
	child := NewRegistry(ws, r.approver)
	_ = child.scheduler.Close()
	child.scheduler, child.ownsScheduler = r.scheduler, false
	child.plan = r.plan
	child.questions = r.questions
	for _, name := range []string{"scheduler_create", "scheduler_list", "scheduler_delete"} {
		delete(child.tools, name)
	}
	for _, name := range []string{"enter_plan_mode", "exit_plan_mode", "ask_user_question"} {
		delete(child.tools, name)
	}
	r.mu.RLock()
	child.readPolicy = r.readPolicy
	if r.webFetch != nil {
		child.webFetch = r.webFetch
		if _, enabled := r.tools["web_fetch"]; enabled {
			child.tools["web_fetch"] = r.webFetch
		} else {
			delete(child.tools, "web_fetch")
		}
	}
	for name, tool := range r.tools {
		if bound, ok := tool.(interface{ WorkspaceBound() bool }); ok && bound.WorkspaceBound() {
			continue
		}
		if _, workspaceBound := child.tools[name]; !workspaceBound {
			child.tools[name] = tool
		}
	}
	for _, name := range []string{"scheduler_create", "scheduler_list", "scheduler_delete"} {
		if tool := r.tools[name]; tool != nil {
			child.tools[name] = tool
		}
	}
	for _, name := range []string{"enter_plan_mode", "exit_plan_mode", "ask_user_question"} {
		if tool := r.tools[name]; tool != nil {
			child.tools[name] = tool
		}
	}
	r.mu.RUnlock()
	return child
}

type WebFetchConfig struct {
	ArtifactDir     string
	ContextWindow   int
	ProxyEndpoint   string
	AllowedDomains  []string
	RestrictDomains bool
}

func (r *Registry) ConfigureWebFetch(config WebFetchConfig) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.webFetch != nil {
		domains := config.AllowedDomains
		if !config.RestrictDomains {
			domains = defaultWebAllowedDomains
		}
		r.webFetch.artifactDir = config.ArtifactDir
		r.webFetch.contextWindow = config.ContextWindow
		r.webFetch.proxyEndpoint = config.ProxyEndpoint
		r.webFetch.restrictDomains = true
		r.webFetch.domainRules = buildWebDomainRules(domains)
	}
	if r.readFile != nil {
		r.readFile.artifactRoot = config.ArtifactDir
	}
}

func (r *Registry) SetWebFetchEnabled(enabled bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if enabled {
		r.tools["web_fetch"] = r.webFetch
	} else {
		delete(r.tools, "web_fetch")
	}
}

func (r *Registry) ConfigureHunkState(artifactDir string) error {
	if artifactDir == "" {
		return errors.New("session artifact directory is required")
	}
	if r.hunks == nil {
		return errors.New("hunk tracker unavailable")
	}
	return r.hunks.configureState(filepath.Join(artifactDir, "hunks.json"))
}

func (r *Registry) SetRewindStore(store *workspace.RewindStore, promptIndex func() int) {
	r.rewind.mu.Lock()
	r.rewind.store, r.rewind.promptIndex = store, promptIndex
	r.rewind.mu.Unlock()
	if r.hunks != nil {
		r.hunks.setPromptIndex(promptIndex)
	}
}

func (r *Registry) BeginGoal(objective string) error {
	if r.goal == nil {
		return errors.New("goal store is unavailable")
	}
	return r.goal.Begin(objective)
}

func (r *Registry) GoalSnapshot() GoalSnapshot {
	if r.goal == nil {
		return GoalSnapshot{}
	}
	return r.goal.Snapshot()
}

func (r *Registry) ResolveGoalVerification(verification GoalVerification) error {
	if r.goal == nil {
		return errors.New("goal store is unavailable")
	}
	return r.goal.ResolveVerification(verification.Achieved, verification.Summary)
}

func (r *Registry) BackgroundTasks() []ProcessSnapshot {
	if r == nil || r.processes == nil {
		return nil
	}
	return r.processes.Snapshots()
}

func (r *Registry) SetProcessObserver(observer ProcessObserver) {
	if r != nil && r.processes != nil {
		r.processes.SetObserver(observer)
	}
}

func (r *Registry) ConfigureSchedulerState(artifactDir string) error {
	if r == nil || r.scheduler == nil {
		return errors.New("scheduler unavailable")
	}
	return r.scheduler.Configure(filepath.Join(artifactDir, "scheduler.json"))
}

func (r *Registry) SetSchedulerObserver(observer SchedulerObserver) {
	if r != nil && r.scheduler != nil {
		r.scheduler.SetObserver(observer)
	}
}

func (r *Registry) ConfigurePlanMode(artifactDir string) error {
	if r == nil || r.plan == nil {
		return errors.New("plan mode unavailable")
	}
	return r.plan.Configure(artifactDir)
}

func (r *Registry) SetPlanModeObserver(observer PlanModeObserver) {
	if r != nil && r.plan != nil {
		r.plan.SetObserver(observer)
	}
}

func (r *Registry) SetUserQuestionObserver(observer UserQuestionObserver) {
	if r != nil && r.questions != nil {
		r.questions.SetObserver(observer)
	}
}

func (r *Registry) ConfigureUserQuestions(timeoutEnabled bool, timeout time.Duration) {
	if r != nil && r.questions != nil {
		r.questions.Configure(timeoutEnabled, timeout)
	}
}

func (r *Registry) SetPlanMode(active bool) error {
	if r == nil || r.plan == nil {
		return errors.New("plan mode unavailable")
	}
	return r.plan.SetActive(active)
}

func (r *Registry) ModeInstructions() string {
	if r == nil || r.plan == nil {
		return ""
	}
	return r.plan.Instructions()
}

func (r *Registry) DeleteScheduledTask(id string) (bool, error) {
	if r == nil || r.scheduler == nil {
		return false, nil
	}
	return r.scheduler.Delete(id)
}

func (r *Registry) KillBackgroundTask(ctx context.Context, id string) (string, error) {
	if r == nil || r.processes == nil {
		return "not_found", nil
	}
	process, err := r.processes.lookup(id)
	if err != nil {
		return "not_found", nil
	}
	select {
	case <-process.done:
		return "already_exited", nil
	default:
	}
	if err := r.processes.Kill(ctx, id); err != nil {
		return "", err
	}
	return "killed", nil
}

func (r *Registry) HunkTracker() *HunkTracker { return r.hunks }

func (r *Registry) SetReadPolicy(approver Approver) {
	r.mu.Lock()
	r.readPolicy = approver
	r.mu.Unlock()
}

func (r *Registry) Close() error {
	var stateErr, processErr, schedulerErr error
	if r.hunks != nil {
		stateErr = r.hunks.saveState()
	}
	if r.processes != nil {
		processErr = r.processes.Close()
	}
	if r.ownsScheduler && r.scheduler != nil {
		schedulerErr = r.scheduler.Close()
	}
	return errors.Join(stateErr, processErr, schedulerErr)
}

func (r *Registry) Register(tool Tool) error {
	if tool == nil {
		return errors.New("tool must not be nil")
	}
	name := tool.Definition().Name
	if name == "" {
		return errors.New("tool name must not be empty")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.tools[name]; exists {
		return fmt.Errorf("tool %q is already registered", name)
	}
	r.tools[name] = tool
	return nil
}

func (r *Registry) Replace(oldNames []string, replacements []Tool) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	old := make(map[string]bool, len(oldNames))
	for _, name := range oldNames {
		old[name] = true
	}
	newNames := make([]string, 0, len(replacements))
	seen := make(map[string]bool, len(replacements))
	for _, replacement := range replacements {
		if replacement == nil {
			return nil, errors.New("replacement tool must not be nil")
		}
		name := replacement.Definition().Name
		if name == "" || seen[name] {
			return nil, fmt.Errorf("invalid duplicate replacement tool %q", name)
		}
		if _, exists := r.tools[name]; exists && !old[name] {
			return nil, fmt.Errorf("tool %q is already registered", name)
		}
		seen[name] = true
		newNames = append(newNames, name)
	}
	for name := range old {
		delete(r.tools, name)
	}
	for index, replacement := range replacements {
		r.tools[newNames[index]] = replacement
	}
	return newNames, nil
}

func (r *Registry) Definitions() []api.ToolDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	definitions := make([]api.ToolDefinition, 0, len(r.tools))
	for _, tool := range r.tools {
		definitions = append(definitions, tool.Definition())
	}
	sort.Slice(definitions, func(i, j int) bool { return definitions[i].Name < definitions[j].Name })
	return definitions
}

func (r *Registry) SnapshotTools() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	items := make([]Tool, 0, len(r.tools))
	for _, tool := range r.tools {
		items = append(items, tool)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Definition().Name < items[j].Definition().Name })
	return items
}

func (r *Registry) HasTool(name string) bool {
	if r == nil {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.tools[name]
	return ok
}

func (r *Registry) FilterMCPServers(keep func(string) bool) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for name, tool := range r.tools {
		marker, ok := tool.(interface{ MCPServerName() string })
		if ok && !keep(marker.MCPServerName()) {
			delete(r.tools, name)
		}
	}
}

// View reuses the parent's concurrency-safe tools while applying a child-only
// allow/deny policy. The returned registry does not own parent resources.
func (r *Registry) View(allowed, denied []string, capability string) *Registry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	allow := toolNameSet(allowed)
	deny := toolNameSet(denied)
	items := make(map[string]Tool)
	for name, tool := range r.tools {
		canonical := strings.ToLower(name)
		if name == "task" || name == "update_goal" || name == "todo_write" || deny[canonical] {
			continue
		}
		if len(allow) > 0 && !allow[canonical] {
			continue
		}
		if !capabilityAllows(capability, name) {
			continue
		}
		items[name] = tool
	}
	return &Registry{tools: items, approver: r.approver, readPolicy: r.readPolicy, hunks: r.hunks, rewind: r.rewind, readFile: r.readFile, webFetch: r.webFetch, subagents: r.subagents, scheduler: r.scheduler, plan: r.plan, questions: r.questions}
}

func toolNameSet(values []string) map[string]bool {
	result := make(map[string]bool, len(values))
	aliases := map[string][]string{
		"read": {"read_file"}, "write": {"write_file", "edit_file", "search_replace"},
		"edit": {"write_file", "edit_file", "search_replace"}, "bash": {"shell", "run_terminal_cmd", "monitor"},
		"grep": {"grep", "search_files"}, "glob": {"list_files", "search_files"},
	}
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" || strings.HasPrefix(value, "agent(") {
			continue
		}
		if expanded := aliases[value]; len(expanded) > 0 {
			for _, name := range expanded {
				result[name] = true
			}
		} else {
			result[value] = true
		}
	}
	return result
}

func capabilityAllows(capability, name string) bool {
	switch strings.ToLower(strings.TrimSpace(capability)) {
	case "read-only", "readonly":
		return name != "write_file" && name != "edit_file" && name != "search_replace" && name != "shell" && name != "run_terminal_cmd" && name != "monitor" && name != "start_command" && name != "kill_command" && !strings.HasPrefix(name, "scheduler_")
	case "read-write", "readwrite":
		return name != "shell" && name != "run_terminal_cmd" && name != "monitor" && name != "start_command" && name != "kill_command"
	case "execute":
		return name != "write_file" && name != "edit_file" && name != "search_replace"
	default:
		return true
	}
}

func (r *Registry) Execute(ctx context.Context, name string, arguments json.RawMessage) (string, error) {
	result, err := r.ExecuteResult(ctx, name, arguments)
	return result.Output, err
}

func (r *Registry) ExecuteResult(ctx context.Context, name string, arguments json.RawMessage) (ExecutionResult, error) {
	r.mu.RLock()
	tool, ok := r.tools[name]
	readPolicy := r.readPolicy
	r.mu.RUnlock()
	if !ok {
		return ExecutionResult{}, fmt.Errorf("unknown tool %q", name)
	}
	arguments, err := normalizeArguments(arguments)
	if err != nil {
		return ExecutionResult{}, err
	}
	if readPolicy != nil {
		if action, detail := readPolicyTarget(name, arguments); action != "" {
			if err := readPolicy.Approve(ctx, action, detail); err != nil {
				return ExecutionResult{}, err
			}
		}
	}
	if r.plan != nil {
		if err := r.plan.Allow(name, arguments, tool); err != nil {
			return ExecutionResult{}, err
		}
	}
	var result ExecutionResult
	var executeErr error
	mutation := mutationPath(name, arguments)
	var before map[string]bool
	if mutation != "" && r.hunks != nil {
		before = r.hunks.snapshot(ctx, mutation)
	}
	if rich, ok := tool.(ResultTool); ok {
		result, executeErr = rich.ExecuteResult(ctx, arguments)
	} else {
		result.Output, executeErr = tool.Execute(ctx, arguments)
	}
	if executeErr == nil && mutation != "" && r.hunks != nil {
		r.hunks.markAgentChanges(ctx, mutation, before)
	}
	return result, executeErr
}

func mutationPath(name string, raw json.RawMessage) string {
	if name != "write_file" && name != "edit_file" && name != "search_replace" {
		return ""
	}
	var values map[string]any
	if json.Unmarshal(raw, &values) != nil {
		return ""
	}
	for _, key := range []string{"target_file", "path", "file_path"} {
		if value, ok := values[key].(string); ok {
			return value
		}
	}
	return ""
}

func readPolicyTarget(name string, raw json.RawMessage) (string, string) {
	var values map[string]any
	if json.Unmarshal(raw, &values) != nil {
		return "", ""
	}
	first := func(keys ...string) string {
		for _, key := range keys {
			if value, ok := values[key].(string); ok && value != "" {
				return value
			}
		}
		return "."
	}
	switch name {
	case "read_file":
		return "read policy", first("target_file", "path")
	case "list_dir", "list_files":
		return "read policy", first("target_directory", "path")
	case "grep", "search_files":
		return "grep policy", first("query", "pattern")
	default:
		return "", ""
	}
}

func normalizeArguments(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return json.RawMessage(`{}`), nil
	}
	if raw[0] != '"' {
		return raw, nil
	}
	var encoded string
	if err := json.Unmarshal(raw, &encoded); err != nil {
		return nil, fmt.Errorf("decode tool arguments string: %w", err)
	}
	return json.RawMessage(encoded), nil
}

func objectSchema(properties map[string]any, required ...string) map[string]any {
	return map[string]any{
		"type":                 "object",
		"properties":           properties,
		"required":             required,
		"additionalProperties": false,
	}
}

type readFileTool struct {
	ws           *workspace.Workspace
	artifactRoot string
}

func (t *readFileTool) Definition() api.ToolDefinition {
	return api.ToolDefinition{
		Type: "function", Name: "read_file",
		Description: "Read text, PPTX, PNG, JPEG, GIF, WebP, or PDF files inside the workspace or the current session artifact directory. PDFs render as page images by default or extract text with format=text. Text results use 1-based LINE_NUMBER→LINE_CONTENT formatting.",
		Parameters: objectSchema(map[string]any{
			"target_file": map[string]any{"type": "string", "description": "Path relative to the workspace, or an absolute current-session artifact path returned by a tool."},
			"offset":      map[string]any{"type": "integer", "description": "1-based starting line; negative values count from the end."},
			"limit":       map[string]any{"type": "integer", "minimum": 1, "maximum": 1000},
			"pages":       map[string]any{"type": "string", "description": "Reserved page range for document formats."},
			"format":      map[string]any{"type": "string", "enum": []string{"image", "text"}},
		}, "target_file"),
	}
}

func (t *readFileTool) Execute(_ context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		TargetFile string `json:"target_file"`
		Path       string `json:"path"`
		Offset     *int   `json:"offset"`
		Limit      int    `json:"limit"`
		StartLine  int    `json:"start_line"`
		EndLine    int    `json:"end_line"`
		Pages      string `json:"pages"`
		Format     string `json:"format"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("decode read_file arguments: %w", err)
	}
	requestedPath := args.TargetFile
	if requestedPath == "" {
		requestedPath = args.Path
	}
	if requestedPath == "" {
		return "", errors.New("target_file is required")
	}
	path, err := t.resolvePath(requestedPath)
	if err != nil {
		return "", err
	}
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open %q: %w", requestedPath, err)
	}
	defer file.Close()

	header := make([]byte, 5)
	headerBytes, _ := file.ReadAt(header, 0)
	isPDF := strings.EqualFold(filepath.Ext(path), ".pdf") || headerBytes == len(header) && bytes.Equal(header, []byte("%PDF-"))
	readLimit := int64(maxReadBytes)
	isArtifact := t.isArtifactPath(path)
	if isArtifact {
		readLimit = maxWebConvertedBytes
	}
	extension := strings.ToLower(filepath.Ext(path))
	if isPDF {
		readLimit = maxPDFBytes
	} else if extension == ".pptx" {
		readLimit = maxPPTXBytes
	}
	reader := io.LimitReader(file, readLimit+1)
	data, err := io.ReadAll(reader)
	if err != nil {
		return "", fmt.Errorf("read %q: %w", requestedPath, err)
	}
	if int64(len(data)) > readLimit {
		return "", fmt.Errorf("file %q exceeds %d bytes", requestedPath, readLimit)
	}
	if isPDF {
		if args.Format != "text" {
			if args.Format != "" && args.Format != "image" {
				return "", fmt.Errorf("invalid PDF format %q; supported values are image and text", args.Format)
			}
			return "", errors.New("PDF image output is not supported yet; use format \"text\"")
		}
		text, err := extractPDFText(data, args.Pages)
		if err != nil {
			return "", fmt.Errorf("read PDF %q: %w", requestedPath, err)
		}
		data = []byte(text)
		if len(data) > maxReadBytes {
			return "", fmt.Errorf("extracted PDF text from %q exceeds %d bytes", requestedPath, maxReadBytes)
		}
	} else if extension == ".pptx" {
		text, err := extractPPTXText(data)
		if err != nil {
			return "", fmt.Errorf("read PPTX %q: %w", requestedPath, err)
		}
		data = []byte(text)
		if len(data) > maxReadBytes {
			return "", fmt.Errorf("extracted PPTX text from %q exceeds %d bytes", requestedPath, maxReadBytes)
		}
	}
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return "", fmt.Errorf("file %q is not UTF-8 text", requestedPath)
	}
	lines := strings.Split(string(data), "\n")
	start := 1
	if args.Offset != nil {
		start = *args.Offset
		if start < 0 {
			start = len(lines) + start + 1
		}
	} else if args.StartLine > 0 {
		start = args.StartLine
	}
	if start < 1 {
		start = 1
	}
	limit := args.Limit
	if limit < 1 {
		limit = 1000
	}
	if limit > 1000 {
		limit = 1000
	}
	end := start + limit - 1
	if args.Offset == nil && args.Limit == 0 && args.EndLine > 0 {
		end = args.EndLine
	}
	if end > len(lines) {
		end = len(lines)
	}
	if start > end || start > len(lines) {
		return "", fmt.Errorf("invalid line range %d..%d for %d-line file", start, end, len(lines))
	}
	var output strings.Builder
	for i := start; i <= end; i++ {
		fmt.Fprintf(&output, "%d→%s\n", i, lines[i-1])
	}
	result := output.String()
	if isArtifact && len(result) > maxOutputBytes {
		result = truncateUTF8(result, maxOutputBytes) + "\n[session artifact output truncated; read another line range or use bash for byte-oriented access]"
	}
	return result, nil
}

func (t *readFileTool) resolvePath(requested string) (string, error) {
	if !filepath.IsAbs(requested) || t.artifactRoot == "" || !pathWithin(t.artifactRoot, requested) {
		return t.ws.Resolve(requested)
	}
	for _, dir := range []string{filepath.Dir(t.artifactRoot), t.artifactRoot} {
		info, err := os.Lstat(dir)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", errors.New("session artifact directory is unavailable or unsafe")
		}
	}
	root, err := filepath.EvalSymlinks(t.artifactRoot)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(requested)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("path escapes workspace and session artifacts")
	}
	return resolved, nil
}

func pathWithin(root, path string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func (t *readFileTool) isArtifactPath(path string) bool {
	if t.artifactRoot == "" {
		return false
	}
	root, err := filepath.EvalSymlinks(t.artifactRoot)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

type listFilesTool struct{ ws *workspace.Workspace }

func (t *listFilesTool) Definition() api.ToolDefinition {
	return api.ToolDefinition{
		Type: "function", Name: "list_files",
		Description: "List files recursively inside a workspace directory. Skips .git and returns at most 1000 files.",
		Parameters: objectSchema(map[string]any{
			"path": map[string]any{"type": "string", "description": "Directory, relative to workspace; defaults to ."},
		}),
	}
}

func (t *listFilesTool) Execute(_ context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("decode list_files arguments: %w", err)
	}
	root, err := t.ws.Resolve(args.Path)
	if err != nil {
		return "", err
	}
	var files []string
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() && (entry.Name() == ".git" || entry.Name() == ".hg") {
			return filepath.SkipDir
		}
		if !entry.IsDir() {
			files = append(files, t.ws.Relative(path))
			if len(files) >= 1000 {
				return errLimitReached
			}
		}
		return nil
	})
	if err != nil && !errors.Is(err, errLimitReached) {
		return "", fmt.Errorf("walk %q: %w", args.Path, err)
	}
	sort.Strings(files)
	return strings.Join(files, "\n"), nil
}

var errLimitReached = errors.New("result limit reached")

type searchFilesTool struct{ ws *workspace.Workspace }

func (t *searchFilesTool) Definition() api.ToolDefinition {
	return api.ToolDefinition{
		Type: "function", Name: "search_files",
		Description: "Search UTF-8 files by regular expression and return path:line:content matches.",
		Parameters: objectSchema(map[string]any{
			"pattern":     map[string]any{"type": "string", "description": "Go regular expression"},
			"path":        map[string]any{"type": "string", "description": "Directory or file; defaults to ."},
			"max_results": map[string]any{"type": "integer", "minimum": 1, "maximum": 500},
		}, "pattern"),
	}
}

func (t *searchFilesTool) Execute(_ context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		Pattern    string `json:"pattern"`
		Path       string `json:"path"`
		MaxResults int    `json:"max_results"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("decode search_files arguments: %w", err)
	}
	pattern, err := regexp.Compile(args.Pattern)
	if err != nil {
		return "", fmt.Errorf("compile pattern: %w", err)
	}
	root, err := t.ws.Resolve(args.Path)
	if err != nil {
		return "", err
	}
	limit := args.MaxResults
	if limit < 1 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	var matches []string
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if entry.Name() == ".git" || entry.Name() == ".hg" {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil || info.Size() > maxReadBytes {
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return nil
		}
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 64<<10), 1<<20)
		lineNo := 0
		for scanner.Scan() {
			lineNo++
			line := scanner.Text()
			if pattern.MatchString(line) {
				matches = append(matches, t.ws.Relative(path)+":"+strconv.Itoa(lineNo)+":"+line)
				if len(matches) >= limit {
					file.Close()
					return errLimitReached
				}
			}
		}
		file.Close()
		return nil
	})
	if err != nil && !errors.Is(err, errLimitReached) {
		return "", fmt.Errorf("search files: %w", err)
	}
	if len(matches) == 0 {
		return "no matches", nil
	}
	return strings.Join(matches, "\n"), nil
}

type writeFileTool struct {
	ws       *workspace.Workspace
	approver Approver
	rewind   *mutationCheckpoint
}

func (t *writeFileTool) Definition() api.ToolDefinition {
	return api.ToolDefinition{
		Type: "function", Name: "write_file",
		Description: "Create or overwrite a UTF-8 text file inside the workspace. Requires write approval.",
		Parameters: objectSchema(map[string]any{
			"path":    map[string]any{"type": "string"},
			"content": map[string]any{"type": "string"},
		}, "path", "content"),
	}
}

func (t *writeFileTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("decode write_file arguments: %w", err)
	}
	if len(args.Content) > maxWriteBytes {
		return "", fmt.Errorf("content exceeds %d bytes", maxWriteBytes)
	}
	path, err := t.ws.Resolve(args.Path)
	if err != nil {
		return "", err
	}
	if err := t.approver.Approve(ctx, "write_file", t.ws.Relative(path)); err != nil {
		return "", err
	}
	if err := t.rewind.before(args.Path); err != nil {
		return "", fmt.Errorf("checkpoint before write: %w", err)
	}
	mode := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	if err := atomicWrite(path, []byte(args.Content), mode); err != nil {
		t.rewind.cancel(args.Path)
		return "", err
	}
	if err := t.rewind.after(args.Path); err != nil {
		return "", fmt.Errorf("checkpoint after write: %w", err)
	}
	return fmt.Sprintf("wrote %d bytes to %s", len(args.Content), t.ws.Relative(path)), nil
}

type editFileTool struct {
	ws       *workspace.Workspace
	approver Approver
	rewind   *mutationCheckpoint
}

func (t *editFileTool) Definition() api.ToolDefinition {
	return api.ToolDefinition{
		Type: "function", Name: "edit_file",
		Description: "Replace exact text in a UTF-8 file. By default old_text must occur exactly once. Requires write approval.",
		Parameters: objectSchema(map[string]any{
			"path":        map[string]any{"type": "string"},
			"old_text":    map[string]any{"type": "string"},
			"new_text":    map[string]any{"type": "string"},
			"replace_all": map[string]any{"type": "boolean"},
		}, "path", "old_text", "new_text"),
	}
}

func (t *editFileTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		Path       string `json:"path"`
		OldText    string `json:"old_text"`
		NewText    string `json:"new_text"`
		ReplaceAll bool   `json:"replace_all"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("decode edit_file arguments: %w", err)
	}
	if args.OldText == "" {
		return "", errors.New("old_text must not be empty")
	}
	path, err := t.ws.Resolve(args.Path)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %q: %w", args.Path, err)
	}
	if len(data) > maxWriteBytes || !utf8.Valid(data) {
		return "", fmt.Errorf("file %q is too large or is not UTF-8", args.Path)
	}
	original := string(data)
	hasCRLF := strings.Contains(original, "\r\n")
	matchText := original
	if hasCRLF {
		matchText = strings.ReplaceAll(matchText, "\r\n", "\n")
	}
	count := strings.Count(matchText, args.OldText)
	var normalized []normalizedMatch
	if count == 0 {
		var ambiguous bool
		normalized, ambiguous = findNormalizedMatches(matchText, args.OldText)
		if ambiguous {
			return "", errors.New("old_text has an ambiguous Unicode-normalized match; provide more ASCII context")
		}
		count = len(normalized)
	}
	if count == 0 {
		return "", errors.New("old_text was not found")
	}
	if !args.ReplaceAll && count != 1 {
		return "", fmt.Errorf("old_text occurs %d times; provide more context or set replace_all", count)
	}
	if err := t.approver.Approve(ctx, "edit_file", fmt.Sprintf("%s (%d replacement(s))", t.ws.Relative(path), count)); err != nil {
		return "", err
	}
	if err := t.rewind.before(args.Path); err != nil {
		return "", fmt.Errorf("checkpoint before edit: %w", err)
	}
	updated := ""
	if len(normalized) > 0 {
		if !args.ReplaceAll {
			normalized = normalized[:1]
		}
		updated = replaceNormalizedMatches(matchText, normalized, args.NewText)
	} else {
		replacements := 1
		if args.ReplaceAll {
			replacements = -1
		}
		updated = strings.Replace(matchText, args.OldText, args.NewText, replacements)
	}
	if hasCRLF {
		updated = strings.ReplaceAll(strings.ReplaceAll(updated, "\r\n", "\n"), "\n", "\r\n")
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if err := atomicWrite(path, []byte(updated), info.Mode().Perm()); err != nil {
		t.rewind.cancel(args.Path)
		return "", err
	}
	if err := t.rewind.after(args.Path); err != nil {
		return "", fmt.Errorf("checkpoint after edit: %w", err)
	}
	method := ""
	if len(normalized) > 0 {
		method = " via Unicode normalization"
	}
	return fmt.Sprintf("edited %s (%d replacement(s)%s)", t.ws.Relative(path), count, method), nil
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, ".gork-go-write-*")
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if err := temp.Chmod(mode); err != nil {
		temp.Close()
		return fmt.Errorf("set temporary file mode: %w", err)
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return fmt.Errorf("write temporary file: %w", err)
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return fmt.Errorf("sync temporary file: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close temporary file: %w", err)
	}
	if err := os.Rename(tempName, path); err != nil {
		return fmt.Errorf("replace %q: %w", path, err)
	}
	return nil
}

type shellTool struct {
	ws       *workspace.Workspace
	approver Approver
	timeout  time.Duration
	rewind   *mutationCheckpoint
}

func (t *shellTool) Definition() api.ToolDefinition {
	return api.ToolDefinition{
		Type: "function", Name: "shell",
		Description: "Run a shell command in the workspace. Commands may affect the system and always require approval unless auto-approved by the user.",
		Parameters: objectSchema(map[string]any{
			"command": map[string]any{"type": "string"},
		}),
	}
}

func (t *shellTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("decode shell arguments: %w", err)
	}
	if strings.TrimSpace(args.Command) == "" {
		return "", errors.New("command must not be empty")
	}
	if err := t.approver.Approve(ctx, "shell", args.Command); err != nil {
		return "", err
	}
	checkpoint, err := t.rewind.beforeWorkspace()
	if err != nil {
		return "", fmt.Errorf("checkpoint before shell command: %w", err)
	}
	commandCtx, cancel := context.WithTimeout(ctx, t.timeout)
	defer cancel()
	var command *exec.Cmd
	if runtime.GOOS == "windows" {
		command = exec.CommandContext(commandCtx, "cmd.exe", "/C", args.Command)
	} else {
		command = exec.CommandContext(commandCtx, "/bin/sh", "-lc", args.Command)
	}
	command.Dir = t.ws.Root()
	var output cappedBuffer
	command.Stdout = &output
	command.Stderr = &output
	err = command.Run()
	checkpointErr := t.rewind.afterWorkspace(checkpoint)
	if commandCtx.Err() != nil {
		return output.String(), errors.Join(fmt.Errorf("command timed out after %s", t.timeout), checkpointErr)
	}
	if err != nil {
		return output.String(), errors.Join(fmt.Errorf("command failed: %w", err), checkpointErr)
	}
	if checkpointErr != nil {
		return output.String(), fmt.Errorf("checkpoint after shell command: %w", checkpointErr)
	}
	if output.Len() == 0 {
		return "command completed with no output", nil
	}
	return output.String(), nil
}

type cappedBuffer struct {
	data      []byte
	truncated bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	original := len(p)
	remaining := maxOutputBytes - len(b.data)
	if remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		b.data = append(b.data, p...)
	}
	if original > remaining {
		b.truncated = true
	}
	return original, nil
}

func (b *cappedBuffer) Len() int { return len(b.data) }

func (b *cappedBuffer) String() string {
	value := string(b.data)
	if b.truncated {
		value += "\n[output truncated]"
	}
	return value
}
