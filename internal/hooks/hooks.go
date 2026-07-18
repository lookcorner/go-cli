package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lookcorner/go-cli/internal/api"
	"github.com/lookcorner/go-cli/internal/compat"
	"github.com/lookcorner/go-cli/internal/plugin"
	"github.com/lookcorner/go-cli/internal/tools"
	"github.com/lookcorner/go-cli/internal/workspace"
)

type Event string

const (
	SessionStart     Event = "session_start"
	PreToolUse       Event = "pre_tool_use"
	PostToolUse      Event = "post_tool_use"
	SessionEnd       Event = "session_end"
	Notification     Event = "notification"
	Stop             Event = "stop"
	StopFailure      Event = "stop_failure"
	UserPromptSubmit Event = "user_prompt_submit"
	PostToolFailure  Event = "post_tool_use_failure"
	PermissionDenied Event = "permission_denied"
	SubagentStart    Event = "subagent_start"
	SubagentStop     Event = "subagent_stop"
	PreCompact       Event = "pre_compact"
	PostCompact      Event = "post_compact"
)

type Spec struct {
	Name      string
	Event     Event
	Type      string
	Matcher   string
	Command   string
	URL       string
	Timeout   time.Duration
	SourceDir string
	Env       map[string]string
	Disabled  bool
	matcher   *regexp.Regexp
}

type Snapshot struct {
	Hooks      []Spec
	LoadErrors []string
}

type Config struct {
	WorkspaceRoot  string
	Compat         compat.Config
	ProjectTrusted bool
	Plugins        []plugin.Plugin
}

type Catalog struct {
	mu           sync.RWMutex
	specs        []Spec
	loadErrors   []string
	disabled     map[string]bool
	disabledPath string
	config       Config
}

var disabledFileMu sync.Mutex
var hookPathsMu sync.Mutex

func Discover(config Config) *Catalog {
	home := os.Getenv("GROK_HOME")
	if home == "" {
		if userHome, err := os.UserHomeDir(); err == nil {
			home = filepath.Join(userHome, ".grok")
		}
	}
	catalog := &Catalog{disabled: make(map[string]bool)}
	if home != "" {
		catalog.disabledPath = filepath.Join(home, "disabled-hooks")
		catalog.disabled = readDisabled(catalog.disabledPath)
	}
	catalog.Reconfigure(config)
	return catalog
}

func DiscoverPlugins(plugins []plugin.Plugin) *Catalog { return Discover(Config{Plugins: plugins}) }

func InspectPlugin(item plugin.Plugin) Snapshot {
	item.Executable = true
	return DiscoverPlugins([]plugin.Plugin{item}).Snapshot()
}

func CountDefined(item plugin.Plugin) int {
	data := item.InlineHooks
	if len(data) == 0 && item.HooksConfig != "" {
		data, _ = os.ReadFile(filepath.Clean(item.HooksConfig))
	}
	var value struct {
		Hooks map[string][]struct {
			Hooks []json.RawMessage `json:"hooks"`
		} `json:"hooks"`
	}
	if json.Unmarshal(data, &value) != nil {
		return 0
	}
	count := 0
	for _, groups := range value.Hooks {
		for _, group := range groups {
			count += len(group.Hooks)
		}
	}
	return count
}

func (c *Catalog) ReplacePlugins(plugins []plugin.Plugin) {
	c.mu.RLock()
	config := c.config
	c.mu.RUnlock()
	config.Plugins = plugins
	c.Reconfigure(config)
}

func (c *Catalog) Reconfigure(config Config) {
	specs, loadErrors := discoverSources(config)
	for _, item := range config.Plugins {
		if !item.Executable || item.HooksConfig == "" && len(item.InlineHooks) == 0 {
			continue
		}
		loaded, warnings := parsePlugin(item)
		specs = append(specs, loaded...)
		loadErrors = append(loadErrors, warnings...)
	}
	c.mu.Lock()
	for index := range specs {
		specs[index].Disabled = c.disabled[specs[index].Name]
	}
	c.specs, c.loadErrors, c.config = specs, loadErrors, config
	c.mu.Unlock()
}

func (c *Catalog) ProjectTrusted() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.config.ProjectTrusted
}

func AddPath(ctx context.Context, path string) error {
	if err := validateCustomPath(path); err != nil {
		return err
	}
	return updatePaths(ctx, path, true)
}

func RemovePath(ctx context.Context, path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("hook path is required")
	}
	return updatePaths(ctx, path, false)
}

func validateCustomPath(path string) error {
	if !filepath.IsAbs(path) {
		return errors.New("hook path must be absolute")
	}
	home := hooksHome()
	if home == "" {
		return errors.New("cannot add a hook path without GROK_HOME or a user home")
	}
	candidate, err := canonicalFuturePath(path)
	if err != nil {
		return fmt.Errorf("resolve hook path: %w", err)
	}
	root, err := canonicalFuturePath(home)
	if err != nil {
		return fmt.Errorf("resolve GROK_HOME: %w", err)
	}
	relative, err := filepath.Rel(root, candidate)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("hook path must be under %s", root)
	}
	return nil
}

func updatePaths(ctx context.Context, path string, add bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	home := hooksHome()
	if home == "" {
		return errors.New("cannot update hook paths without GROK_HOME or a user home")
	}
	hookPathsMu.Lock()
	defer hookPathsMu.Unlock()
	file := filepath.Join(home, "hooks-paths")
	data, err := os.ReadFile(file)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	var lines []string
	found := false
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if line == path {
			found = true
			if !add {
				continue
			}
		}
		lines = append(lines, line)
	}
	if add && !found {
		lines = append(lines, path)
	}
	return writeLines(ctx, file, lines)
}

func (c *Catalog) Snapshot() Snapshot {
	c.mu.RLock()
	hooks := make([]Spec, len(c.specs))
	copy(hooks, c.specs)
	loadErrors := append([]string(nil), c.loadErrors...)
	disabledPath := c.disabledPath
	c.mu.RUnlock()
	if disabledPath != "" {
		disabled := readDisabled(disabledPath)
		for index := range hooks {
			hooks[index].Disabled = disabled[hooks[index].Name]
		}
	}
	return Snapshot{Hooks: hooks, LoadErrors: loadErrors}
}

func (c *Catalog) SetDisabled(ctx context.Context, names []string, disabled bool) error {
	c.mu.RLock()
	path := c.disabledPath
	c.mu.RUnlock()
	if path == "" {
		return errors.New("cannot persist disabled hooks without GROK_HOME or a user home")
	}
	disabledFileMu.Lock()
	defer disabledFileMu.Unlock()
	current := readDisabled(path)
	for _, name := range names {
		if disabled {
			current[name] = true
		} else {
			delete(current, name)
		}
	}
	if err := writeDisabled(ctx, path, current); err != nil {
		return err
	}
	c.mu.Lock()
	c.disabled = current
	for index := range c.specs {
		c.specs[index].Disabled = current[c.specs[index].Name]
	}
	c.mu.Unlock()
	return nil
}

type Runtime struct {
	Catalog       *Catalog
	WorkspaceRoot string
	SessionID     string
	Model         string
	SubagentType  string
}

type DeniedError struct {
	Hook   string
	Reason string
}

func (e *DeniedError) Error() string {
	return fmt.Sprintf("hook %s denied tool use: %s", e.Hook, e.Reason)
}

func (r *Runtime) SessionStarted(ctx context.Context) {
	r.dispatch(ctx, SessionStart, "", map[string]any{"source": "startup", "modelId": r.Model, "agentType": r.SubagentType}, false)
}

func (r *Runtime) UserPromptSubmitted(ctx context.Context, prompt string) {
	r.dispatch(ctx, UserPromptSubmit, "", map[string]any{"prompt": prompt}, false)
}

func (r *Runtime) BeforeTool(ctx context.Context, call api.ToolCall) error {
	return r.dispatch(ctx, PreToolUse, call.Name, map[string]any{
		"toolName": call.Name, "toolUseId": call.CallID, "toolInput": validJSON(call.Arguments), "toolInputTruncated": false, "subagentType": r.SubagentType,
	}, true)
}

func (r *Runtime) AfterTool(ctx context.Context, call api.ToolCall, result tools.ExecutionResult, toolErr error) {
	if toolErr != nil {
		if tools.IsPermissionDenied(toolErr) {
			r.dispatch(ctx, PermissionDenied, call.Name, map[string]any{
				"toolName": call.Name, "toolUseId": call.CallID, "toolInput": validJSON(call.Arguments),
				"toolInputTruncated": false,
			}, false)
			return
		}
		r.dispatch(ctx, PostToolFailure, call.Name, map[string]any{
			"toolName": call.Name, "toolUseId": call.CallID, "toolInput": validJSON(call.Arguments),
			"toolInputTruncated": false, "error": toolErr.Error(), "subagentType": r.SubagentType,
		}, false)
		return
	}
	r.dispatch(ctx, PostToolUse, call.Name, map[string]any{
		"toolName": call.Name, "toolUseId": call.CallID, "toolInput": json.RawMessage(call.Arguments),
		"toolResult": result.Output, "toolInputTruncated": false, "toolResultTruncated": false, "isBackgrounded": false, "subagentType": r.SubagentType,
	}, false)
}

func validJSON(value []byte) json.RawMessage {
	if json.Valid(value) {
		return json.RawMessage(value)
	}
	return json.RawMessage("null")
}

func (r *Runtime) Stopped(ctx context.Context, reason string, runErr error) {
	if runErr != nil {
		r.dispatch(ctx, StopFailure, "", map[string]any{"error": runErr.Error()}, false)
		return
	}
	r.dispatch(ctx, Stop, "", map[string]any{"reason": reason}, false)
}

func (r *Runtime) BeforeCompact(ctx context.Context, source string) {
	r.dispatch(ctx, PreCompact, "", map[string]any{"source": source}, false)
}

func (r *Runtime) AfterCompact(ctx context.Context, source string) {
	r.dispatch(ctx, PostCompact, "", map[string]any{"source": source}, false)
}

func (r *Runtime) SubagentStarted(ctx context.Context, id, agentType, description string) {
	r.dispatch(ctx, SubagentStart, "", map[string]any{"subagentId": id, "subagentType": agentType, "description": description}, false)
}

func (r *Runtime) SubagentEnded(ctx context.Context, id, agentType, status string, durationMS int64) {
	r.dispatch(ctx, SubagentStop, "", map[string]any{"subagentId": id, "subagentType": agentType, "description": status, "durationMs": durationMS}, false)
}

func (r *Runtime) SessionEnded(ctx context.Context, reason string) {
	r.dispatch(ctx, SessionEnd, "", map[string]any{"reason": reason}, false)
}

func (r *Runtime) dispatch(ctx context.Context, event Event, toolName string, payload map[string]any, blocking bool) error {
	if r == nil || r.Catalog == nil {
		return nil
	}
	snapshot := r.Catalog.Snapshot()
	for _, spec := range snapshot.Hooks {
		if spec.Disabled || spec.Event != event || spec.matcher != nil && !spec.matcher.MatchString(toolName) {
			continue
		}
		envelope := map[string]any{
			"hookEventName": event, "sessionId": r.SessionID, "cwd": r.WorkspaceRoot,
			"workspaceRoot": r.WorkspaceRoot, "timestamp": time.Now().UTC().Format(time.RFC3339Nano),
		}
		for key, value := range payload {
			envelope[key] = value
		}
		decision, reason, err := run(ctx, spec, envelope, r.WorkspaceRoot, r.SessionID)
		if err == nil && blocking && decision == "deny" {
			if strings.TrimSpace(reason) == "" {
				reason = "blocked by hook"
			}
			return &DeniedError{Hook: spec.Name, Reason: reason}
		}
	}
	return nil
}

type fileConfig struct {
	Hooks map[string][]matcherGroup `json:"hooks"`
}

type matcherGroup struct {
	Matcher string       `json:"matcher"`
	Hooks   []rawHandler `json:"hooks"`
}

type rawHandler struct {
	Type    string            `json:"type"`
	Command string            `json:"command"`
	URL     string            `json:"url"`
	Timeout uint64            `json:"timeout"`
	Env     map[string]string `json:"env"`
}

var eventNames = map[string]Event{
	"SessionStart": SessionStart, "session_start": SessionStart,
	"PreToolUse": PreToolUse, "pre_tool_use": PreToolUse,
	"PostToolUse": PostToolUse, "post_tool_use": PostToolUse,
	"SessionEnd": SessionEnd, "session_end": SessionEnd,
	"Notification": Notification, "notification": Notification,
	"Stop": Stop, "stop": Stop,
	"StopFailure": StopFailure, "stop_failure": StopFailure, "stopFailure": StopFailure,
	"UserPromptSubmit": UserPromptSubmit, "user_prompt_submit": UserPromptSubmit,
	"PostToolUseFailure": PostToolFailure, "post_tool_use_failure": PostToolFailure, "postToolUseFailure": PostToolFailure,
	"PermissionDenied": PermissionDenied, "permission_denied": PermissionDenied, "permissionDenied": PermissionDenied,
	"SubagentStart": SubagentStart, "subagent_start": SubagentStart,
	"SubagentStop": SubagentStop, "subagent_stop": SubagentStop,
	"SubagentEnd": SubagentStop, "subagent_end": SubagentStop,
	"PreCompact": PreCompact, "pre_compact": PreCompact, "preCompact": PreCompact,
	"PostCompact": PostCompact, "post_compact": PostCompact, "postCompact": PostCompact,
	"sessionStart": SessionStart, "preToolUse": PreToolUse, "postToolUse": PostToolUse, "sessionEnd": SessionEnd,
	"beforeShellExecution": PreToolUse, "beforeMCPExecution": PreToolUse, "beforeReadFile": PreToolUse,
	"afterShellExecution": PostToolUse, "afterMCPExecution": PostToolUse, "afterFileEdit": PostToolUse,
	"afterAgentResponse": PostToolUse, "afterAgentThought": PostToolUse, "beforeSubmitPrompt": UserPromptSubmit,
}

var reservedEnv = map[string]bool{
	"GROK_HOOK_EVENT": true, "GROK_HOOK_NAME": true, "GROK_SESSION_ID": true,
	"GROK_WORKSPACE_ROOT": true, "CLAUDE_PROJECT_DIR": true,
}

type source struct {
	path   string
	prefix string
}

func discoverSources(config Config) ([]Spec, []string) {
	if config.WorkspaceRoot == "" {
		return nil, nil
	}
	home, _ := os.UserHomeDir()
	grokHome := os.Getenv("GROK_HOME")
	if grokHome == "" && home != "" {
		grokHome = filepath.Join(home, ".grok")
	}
	var sources []source
	if config.Compat.Claude.Hooks && home != "" {
		sources = append(sources,
			source{filepath.Join(home, ".claude", "settings.json"), "global/"},
			source{filepath.Join(home, ".claude", "settings.local.json"), "global/"},
		)
	}
	if grokHome != "" {
		sources = append(sources, source{filepath.Join(grokHome, "hooks"), "global/"})
		if data, err := os.ReadFile(filepath.Join(grokHome, "hooks-paths")); err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				if line = strings.TrimSpace(line); line != "" {
					sources = append(sources, source{line, "global/"})
				}
			}
		}
	}
	if config.Compat.Cursor.Hooks && home != "" {
		sources = append(sources, source{filepath.Join(home, ".cursor", "hooks.json"), "global/"})
	}
	if config.ProjectTrusted {
		root, ok := workspace.FindGitRoot(config.WorkspaceRoot)
		if !ok {
			return loadSources(sources)
		}
		if config.Compat.Claude.Hooks {
			sources = append(sources,
				source{filepath.Join(root, ".claude", "settings.json"), "project/"},
				source{filepath.Join(root, ".claude", "settings.local.json"), "project/"},
			)
		}
		sources = append(sources, source{filepath.Join(root, ".grok", "hooks"), "project/"})
		if config.Compat.Cursor.Hooks {
			sources = append(sources, source{filepath.Join(root, ".cursor", "hooks.json"), "project/"})
		}
	}
	return loadSources(sources)
}

func loadSources(sources []source) ([]Spec, []string) {
	var specs []Spec
	var warnings []string
	seen := make(map[string]bool)
	for _, item := range sources {
		loaded, loadWarnings := loadSource(item)
		warnings = append(warnings, loadWarnings...)
		for _, spec := range loaded {
			key := string(spec.Event) + "\x00" + spec.Command + "\x00" + spec.URL + "\x00" + spec.Matcher
			if !seen[key] {
				seen[key] = true
				specs = append(specs, spec)
			}
		}
	}
	return specs, warnings
}

func loadSource(item source) ([]Spec, []string) {
	info, err := os.Stat(item.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, []string{fmt.Sprintf("read hooks source %s: %v", item.path, err)}
	}
	paths := []string{item.path}
	if info.IsDir() {
		entries, err := os.ReadDir(item.path)
		if err != nil {
			return nil, []string{fmt.Sprintf("read hooks source %s: %v", item.path, err)}
		}
		paths = paths[:0]
		for _, entry := range entries {
			name := entry.Name()
			info, err := entry.Info()
			if err != nil || !info.Mode().IsRegular() || filepath.Ext(name) != ".json" || strings.HasPrefix(name, ".") || strings.HasSuffix(name, "~") || strings.HasSuffix(name, ".swp") || strings.HasSuffix(name, ".swo") {
				continue
			}
			paths = append(paths, filepath.Join(item.path, name))
		}
		sort.Strings(paths)
	} else if !info.Mode().IsRegular() {
		return nil, nil
	}
	var specs []Spec
	var warnings []string
	for _, path := range paths {
		data, err := os.ReadFile(filepath.Clean(path))
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("read hooks file %s: %v", path, err))
			continue
		}
		loaded, loadWarnings := parseData(data, path, item.prefix, path, nil, false)
		specs = append(specs, loaded...)
		warnings = append(warnings, loadWarnings...)
	}
	return specs, warnings
}

func parsePlugin(item plugin.Plugin) ([]Spec, []string) {
	env := map[string]string{
		"GROK_PLUGIN_ROOT": item.Root, "CLAUDE_PLUGIN_ROOT": item.Root,
		"GROK_PLUGIN_DATA": item.DataDir, "CLAUDE_PLUGIN_DATA": item.DataDir,
	}
	if len(item.InlineHooks) > 0 {
		return parseData(item.InlineHooks, filepath.Join(item.Root, "plugin.json"), "plugin/"+item.Name+"/", "plugin "+item.Name, env, true)
	}
	data, err := os.ReadFile(filepath.Clean(item.HooksConfig))
	if err != nil {
		return nil, []string{fmt.Sprintf("plugin %s: read hooks: %v", item.Name, err)}
	}
	return parseData(data, item.HooksConfig, "plugin/"+item.Name+"/", "plugin "+item.Name, env, true)
}

func parseData(data []byte, sourcePath, prefix, label string, injectedEnv map[string]string, pluginSource bool) ([]Spec, []string) {
	var configured fileConfig
	if err := json.Unmarshal(data, &configured); err != nil {
		return nil, []string{fmt.Sprintf("%s: parse hooks: %v", label, err)}
	}
	var specs []Spec
	var warnings []string
	stem := strings.TrimSuffix(filepath.Base(sourcePath), filepath.Ext(sourcePath))
	for rawEvent, groups := range configured.Hooks {
		event, ok := eventNames[rawEvent]
		if !ok {
			warnings = append(warnings, fmt.Sprintf("%s: skipped unsupported event %q", label, rawEvent))
			continue
		}
		if pluginSource && !pluginEvent(event) {
			warnings = append(warnings, fmt.Sprintf("%s: skipped unsupported event %q", label, rawEvent))
			continue
		}
		for groupIndex, group := range groups {
			if group.Matcher != "" && lifecycleEvent(event) {
				warnings = append(warnings, fmt.Sprintf("%s: matcher is not allowed for %s", label, event))
				continue
			}
			var matcher *regexp.Regexp
			if group.Matcher != "" {
				var matcherErr error
				matcher, matcherErr = regexp.Compile(group.Matcher)
				if matcherErr != nil {
					warnings = append(warnings, fmt.Sprintf("%s: invalid matcher %q: %v", label, group.Matcher, matcherErr))
					continue
				}
			}
			for hookIndex, handler := range group.Hooks {
				if handler.Type != "command" && handler.Type != "http" {
					warnings = append(warnings, fmt.Sprintf("%s: unsupported hook handler %q", label, handler.Type))
					continue
				}
				if handler.Type == "command" && strings.TrimSpace(handler.Command) == "" || handler.Type == "http" && strings.TrimSpace(handler.URL) == "" {
					warnings = append(warnings, fmt.Sprintf("%s: %s hook target is required", label, handler.Type))
					continue
				}
				timeout := 5 * time.Second
				if handler.Timeout > 0 {
					timeout = time.Duration(min(handler.Timeout, 300)) * time.Second
				}
				env := make(map[string]string, len(handler.Env)+len(injectedEnv))
				for key, value := range handler.Env {
					if !reservedEnv[key] {
						env[key] = value
					}
				}
				for key, value := range injectedEnv {
					env[key] = value
				}
				specs = append(specs, Spec{
					Name:  fmt.Sprintf("%s%s:%s[%d].hooks[%d]", prefix, stem, event, groupIndex, hookIndex),
					Event: event, Type: handler.Type, Matcher: group.Matcher, Command: handler.Command,
					URL: handler.URL, Timeout: timeout, SourceDir: filepath.Dir(sourcePath), Env: env,
					matcher: matcher,
				})
			}
		}
	}
	sort.SliceStable(specs, func(i, j int) bool { return specs[i].Name < specs[j].Name })
	return specs, warnings
}

func lifecycleEvent(event Event) bool {
	return event == SessionStart || event == SessionEnd || event == Stop || event == StopFailure || event == UserPromptSubmit || event == PreCompact || event == PostCompact
}

func pluginEvent(event Event) bool {
	return event == SessionStart || event == PreToolUse || event == PostToolUse || event == SessionEnd || event == Notification || event == Stop || event == UserPromptSubmit || event == SubagentStart || event == SubagentStop
}

func run(ctx context.Context, spec Spec, envelope any, workspaceRoot, sessionID string) (string, string, error) {
	payload, err := json.Marshal(envelope)
	if err != nil {
		return "", "", err
	}
	runCtx, cancel := context.WithTimeout(ctx, spec.Timeout)
	defer cancel()
	if spec.Type == "http" {
		return runHTTP(runCtx, spec, payload)
	}
	command, args := commandLine(spec)
	cmd := exec.CommandContext(runCtx, command, args...)
	cmd.Dir = workspaceRoot
	cmd.Stdin = bytes.NewReader(payload)
	stdout, stderr := &limitedBuffer{limit: 64 << 10}, &limitedBuffer{limit: 64 << 10}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.Env = append(os.Environ(), envList(spec.Env)...)
	cmd.Env = append(cmd.Env,
		"GROK_HOOK_EVENT="+string(spec.Event), "GROK_HOOK_NAME="+spec.Name,
		"GROK_SESSION_ID="+sessionID, "GROK_WORKSPACE_ROOT="+workspaceRoot,
		"CLAUDE_PROJECT_DIR="+workspaceRoot,
	)
	runErr := cmd.Run()
	var result struct {
		Decision string `json:"decision"`
		Reason   string `json:"reason"`
	}
	if json.Unmarshal(stdout.Bytes(), &result) == nil && result.Decision == "deny" {
		return result.Decision, result.Reason, nil
	}
	if runErr != nil {
		return "", "", fmt.Errorf("run hook %s: %w: %s", spec.Name, runErr, strings.TrimSpace(stderr.String()))
	}
	return result.Decision, result.Reason, nil
}

func commandLine(spec Spec) (string, []string) {
	if strings.ContainsAny(spec.Command, " |&;<>$") || strings.HasPrefix(spec.Command, "~") {
		if runtime.GOOS == "windows" {
			return "cmd", []string{"/C", spec.Command}
		}
		return "sh", []string{"-c", spec.Command}
	}
	command := spec.Command
	if !filepath.IsAbs(command) {
		command = filepath.Join(spec.SourceDir, command)
	}
	return command, nil
}

func runHTTP(ctx context.Context, spec Spec, payload []byte) (string, string, error) {
	actualURL := os.Expand(spec.URL, func(key string) string {
		if value, ok := spec.Env[key]; ok {
			return value
		}
		return os.Getenv(key)
	})
	parsed, err := url.Parse(actualURL)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" {
		return "", "", fmt.Errorf("invalid hook URL %q", spec.URL)
	}
	if err := validateHTTPHost(ctx, parsed); err != nil {
		return "", "", err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, actualURL, bytes.NewReader(payload))
	if err != nil {
		return "", "", err
	}
	request.Header.Set("Content-Type", "application/json")
	client := &http.Client{CheckRedirect: func(request *http.Request, _ []*http.Request) error {
		if request.URL.Scheme != "https" {
			return errors.New("hook redirect must use HTTPS")
		}
		return validateHTTPHost(request.Context(), request.URL)
	}}
	response, err := client.Do(request)
	if err != nil {
		return "", "", err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	if err != nil {
		return "", "", err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", "", fmt.Errorf("hook HTTP status %d", response.StatusCode)
	}
	var result struct {
		Decision string `json:"decision"`
		Reason   string `json:"reason"`
	}
	if err := json.Unmarshal(body, &result); err != nil && len(bytes.TrimSpace(body)) > 0 {
		return "", "", err
	}
	return result.Decision, result.Reason, nil
}

func validateHTTPHost(ctx context.Context, parsed *url.URL) error {
	host := parsed.Hostname()
	if ip := net.ParseIP(host); ip != nil {
		if blockedIP(ip) {
			return errors.New("hook URL resolves to a blocked private or internal address")
		}
		return nil
	}
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil || len(addresses) == 0 {
		return fmt.Errorf("resolve hook host: %w", err)
	}
	for _, address := range addresses {
		if blockedIP(address.IP) {
			return errors.New("hook URL resolves to a blocked private or internal address")
		}
	}
	return nil
}

func blockedIP(ip net.IP) bool {
	if ip.IsLoopback() {
		return false
	}
	if ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
		return true
	}
	v4 := ip.To4()
	return v4 != nil && v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127
}

type limitedBuffer struct {
	buf   bytes.Buffer
	limit int
}

func (b *limitedBuffer) Write(value []byte) (int, error) {
	original := len(value)
	remaining := b.limit - b.buf.Len()
	if remaining > 0 {
		if len(value) > remaining {
			value = value[:remaining]
		}
		_, _ = b.buf.Write(value)
	}
	return original, nil
}

func (b *limitedBuffer) Bytes() []byte  { return b.buf.Bytes() }
func (b *limitedBuffer) String() string { return b.buf.String() }

func envList(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+values[key])
	}
	return result
}

func readDisabled(path string) map[string]bool {
	result := make(map[string]bool)
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return result
	}
	for _, line := range strings.Split(string(data), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			result[line] = true
		}
	}
	return result
}

func writeDisabled(ctx context.Context, path string, disabled map[string]bool) error {
	names := make([]string, 0, len(disabled))
	for name := range disabled {
		names = append(names, name)
	}
	sort.Strings(names)
	return writeLines(ctx, path, names)
}

func writeLines(ctx context.Context, path string, lines []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".hooks-state-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	content := strings.Join(lines, "\n")
	if len(lines) > 0 {
		content += "\n"
	}
	_, err = io.WriteString(temporary, content)
	closeErr := temporary.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(temporaryPath, path)
}

func hooksHome() string {
	if home := strings.TrimSpace(os.Getenv("GROK_HOME")); home != "" {
		return filepath.Clean(home)
	}
	home, _ := os.UserHomeDir()
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".grok")
}

func canonicalFuturePath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	base := filepath.Clean(absolute)
	var tail []string
	for {
		if _, err := os.Lstat(base); err == nil {
			real, err := filepath.EvalSymlinks(base)
			if err != nil {
				return "", err
			}
			for index := len(tail) - 1; index >= 0; index-- {
				real = filepath.Join(real, tail[index])
			}
			return filepath.Clean(real), nil
		} else if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(base)
		if parent == base {
			return "", errors.New("no existing path ancestor")
		}
		tail = append(tail, filepath.Base(base))
		base = parent
	}
}
