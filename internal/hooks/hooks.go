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
	"github.com/lookcorner/go-cli/internal/plugin"
	"github.com/lookcorner/go-cli/internal/tools"
)

type Event string

const (
	SessionStart     Event = "session_start"
	PreToolUse       Event = "pre_tool_use"
	PostToolUse      Event = "post_tool_use"
	SessionEnd       Event = "session_end"
	Notification     Event = "notification"
	Stop             Event = "stop"
	UserPromptSubmit Event = "user_prompt_submit"
	SubagentStart    Event = "subagent_start"
	SubagentEnd      Event = "subagent_end"
)

type Spec struct {
	Name       string
	Event      Event
	Type       string
	Matcher    string
	Command    string
	URL        string
	Timeout    time.Duration
	SourceDir  string
	Env        map[string]string
	Disabled   bool
	PluginName string
	matcher    *regexp.Regexp
}

type Snapshot struct {
	Hooks      []Spec
	LoadErrors []string
}

type Catalog struct {
	mu           sync.RWMutex
	specs        []Spec
	loadErrors   []string
	disabled     map[string]bool
	disabledPath string
}

var disabledFileMu sync.Mutex

func DiscoverPlugins(plugins []plugin.Plugin) *Catalog {
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
	catalog.ReplacePlugins(plugins)
	return catalog
}

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
	var specs []Spec
	var loadErrors []string
	for _, item := range plugins {
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
	c.specs, c.loadErrors = specs, loadErrors
	c.mu.Unlock()
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
}

type DeniedError struct {
	Hook   string
	Reason string
}

func (e *DeniedError) Error() string {
	return fmt.Sprintf("hook %s denied tool use: %s", e.Hook, e.Reason)
}

func (r *Runtime) SessionStarted(ctx context.Context) {
	r.dispatch(ctx, SessionStart, "", map[string]any{"source": "startup", "modelId": r.Model}, false)
}

func (r *Runtime) UserPromptSubmitted(ctx context.Context, prompt string) {
	r.dispatch(ctx, UserPromptSubmit, "", map[string]any{"prompt": prompt}, false)
}

func (r *Runtime) BeforeTool(ctx context.Context, call api.ToolCall) error {
	input := json.RawMessage(call.Arguments)
	if !json.Valid(input) {
		input = json.RawMessage("null")
	}
	return r.dispatch(ctx, PreToolUse, call.Name, map[string]any{
		"toolName": call.Name, "toolUseId": call.CallID, "toolInput": input, "toolInputTruncated": false,
	}, true)
}

func (r *Runtime) AfterTool(ctx context.Context, call api.ToolCall, result tools.ExecutionResult, toolErr error) {
	value := any(result.Output)
	if toolErr != nil {
		value = map[string]any{"error": toolErr.Error()}
	}
	r.dispatch(ctx, PostToolUse, call.Name, map[string]any{
		"toolName": call.Name, "toolUseId": call.CallID, "toolInput": json.RawMessage(call.Arguments),
		"toolResult": value, "toolInputTruncated": false, "toolResultTruncated": false, "isBackgrounded": false,
	}, false)
}

func (r *Runtime) Stopped(ctx context.Context, reason string) {
	r.dispatch(ctx, Stop, "", map[string]any{"reason": reason}, false)
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
	"UserPromptSubmit": UserPromptSubmit, "user_prompt_submit": UserPromptSubmit,
	"SubagentStart": SubagentStart, "subagent_start": SubagentStart,
	"SubagentEnd": SubagentEnd, "subagent_end": SubagentEnd,
}

var reservedEnv = map[string]bool{
	"GROK_HOOK_EVENT": true, "GROK_HOOK_NAME": true, "GROK_SESSION_ID": true,
	"GROK_WORKSPACE_ROOT": true, "CLAUDE_PROJECT_DIR": true,
}

func parsePlugin(item plugin.Plugin) ([]Spec, []string) {
	if len(item.InlineHooks) > 0 {
		return parseData(item, item.InlineHooks, filepath.Join(item.Root, "plugin.json"))
	}
	data, err := os.ReadFile(filepath.Clean(item.HooksConfig))
	if err != nil {
		return nil, []string{fmt.Sprintf("plugin %s: read hooks: %v", item.Name, err)}
	}
	return parseData(item, data, item.HooksConfig)
}

func parseData(item plugin.Plugin, data []byte, sourcePath string) ([]Spec, []string) {
	var configured fileConfig
	if err := json.Unmarshal(data, &configured); err != nil {
		return nil, []string{fmt.Sprintf("plugin %s: parse hooks: %v", item.Name, err)}
	}
	var specs []Spec
	var warnings []string
	stem := strings.TrimSuffix(filepath.Base(sourcePath), filepath.Ext(sourcePath))
	for rawEvent, groups := range configured.Hooks {
		event, ok := eventNames[rawEvent]
		if !ok {
			warnings = append(warnings, fmt.Sprintf("plugin %s: skipped unsupported event %q", item.Name, rawEvent))
			continue
		}
		for groupIndex, group := range groups {
			if group.Matcher != "" && lifecycleEvent(event) {
				warnings = append(warnings, fmt.Sprintf("plugin %s: matcher is not allowed for %s", item.Name, event))
				continue
			}
			var matcher *regexp.Regexp
			if group.Matcher != "" {
				var matcherErr error
				matcher, matcherErr = regexp.Compile(group.Matcher)
				if matcherErr != nil {
					warnings = append(warnings, fmt.Sprintf("plugin %s: invalid matcher %q: %v", item.Name, group.Matcher, matcherErr))
					continue
				}
			}
			for hookIndex, handler := range group.Hooks {
				if handler.Type != "command" && handler.Type != "http" {
					warnings = append(warnings, fmt.Sprintf("plugin %s: unsupported hook handler %q", item.Name, handler.Type))
					continue
				}
				if handler.Type == "command" && strings.TrimSpace(handler.Command) == "" || handler.Type == "http" && strings.TrimSpace(handler.URL) == "" {
					warnings = append(warnings, fmt.Sprintf("plugin %s: %s hook target is required", item.Name, handler.Type))
					continue
				}
				timeout := 5 * time.Second
				if handler.Timeout > 0 {
					timeout = time.Duration(min(handler.Timeout, 300)) * time.Second
				}
				env := make(map[string]string, len(handler.Env)+4)
				for key, value := range handler.Env {
					if !reservedEnv[key] {
						env[key] = value
					}
				}
				for _, key := range []string{"GROK_PLUGIN_ROOT", "CLAUDE_PLUGIN_ROOT"} {
					env[key] = item.Root
				}
				for _, key := range []string{"GROK_PLUGIN_DATA", "CLAUDE_PLUGIN_DATA"} {
					env[key] = item.DataDir
				}
				specs = append(specs, Spec{
					Name:  fmt.Sprintf("plugin/%s/%s:%s[%d].hooks[%d]", item.Name, stem, event, groupIndex, hookIndex),
					Event: event, Type: handler.Type, Matcher: group.Matcher, Command: handler.Command,
					URL: handler.URL, Timeout: timeout, SourceDir: filepath.Dir(sourcePath), Env: env,
					PluginName: item.Name, matcher: matcher,
				})
			}
		}
	}
	sort.SliceStable(specs, func(i, j int) bool { return specs[i].Name < specs[j].Name })
	return specs, warnings
}

func lifecycleEvent(event Event) bool {
	return event == SessionStart || event == SessionEnd || event == Stop || event == UserPromptSubmit
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
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	names := make([]string, 0, len(disabled))
	for name := range disabled {
		names = append(names, name)
	}
	sort.Strings(names)
	temporary, err := os.CreateTemp(filepath.Dir(path), ".disabled-hooks-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	_, err = io.WriteString(temporary, strings.Join(names, "\n")+map[bool]string{true: "\n"}[len(names) > 0])
	closeErr := temporary.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(temporaryPath, path)
}
