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
	"time"
	"unicode/utf8"

	"github.com/lookcorner/go-cli/internal/api"
	"github.com/lookcorner/go-cli/internal/workspace"
)

const (
	maxReadBytes   = 2 << 20
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
		return fmt.Errorf("permission denied for %s", action)
	case PermissionPrompt:
		if a.Input == nil || a.Output == nil {
			return fmt.Errorf("permission prompt unavailable for %s", action)
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
		return fmt.Errorf("permission denied for %s", action)
	default:
		return fmt.Errorf("unknown permission mode %q", a.Mode)
	}
}

type Tool interface {
	Definition() api.ToolDefinition
	Execute(context.Context, json.RawMessage) (string, error)
}

type Registry struct {
	tools     map[string]Tool
	processes *ProcessManager
	goal      *GoalStore
}

func NewRegistry(ws *workspace.Workspace, approver Approver) *Registry {
	processes := NewProcessManager(ws, approver)
	todos := newTodoStore()
	goal := NewGoalStore()
	items := []Tool{
		&readFileTool{ws: ws},
		&listFilesTool{ws: ws},
		&searchFilesTool{ws: ws},
		&writeFileTool{ws: ws, approver: approver},
		&editFileTool{ws: ws, approver: approver},
		&shellTool{ws: ws, approver: approver, timeout: 2 * time.Minute},
		&startCommandTool{manager: processes},
		&commandOutputTool{manager: processes},
		&killCommandTool{manager: processes},
		&runTerminalCommandTool{manager: processes},
		&taskOutputTool{manager: processes},
		&killTaskTool{manager: processes},
		&listDirTool{ws: ws},
		&grepTool{ws: ws},
		&searchReplaceTool{ws: ws, approver: approver},
		&todoWriteTool{store: todos},
		&updateGoalTool{store: goal},
	}
	registry := &Registry{tools: make(map[string]Tool, len(items)), processes: processes, goal: goal}
	for _, item := range items {
		registry.tools[item.Definition().Name] = item
	}
	return registry
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

func (r *Registry) Close() error {
	if r.processes == nil {
		return nil
	}
	return r.processes.Close()
}

func (r *Registry) Register(tool Tool) error {
	if tool == nil {
		return errors.New("tool must not be nil")
	}
	name := tool.Definition().Name
	if name == "" {
		return errors.New("tool name must not be empty")
	}
	if _, exists := r.tools[name]; exists {
		return fmt.Errorf("tool %q is already registered", name)
	}
	r.tools[name] = tool
	return nil
}

func (r *Registry) Definitions() []api.ToolDefinition {
	definitions := make([]api.ToolDefinition, 0, len(r.tools))
	for _, tool := range r.tools {
		definitions = append(definitions, tool.Definition())
	}
	sort.Slice(definitions, func(i, j int) bool { return definitions[i].Name < definitions[j].Name })
	return definitions
}

func (r *Registry) Execute(ctx context.Context, name string, arguments json.RawMessage) (string, error) {
	tool, ok := r.tools[name]
	if !ok {
		return "", fmt.Errorf("unknown tool %q", name)
	}
	arguments, err := normalizeArguments(arguments)
	if err != nil {
		return "", err
	}
	return tool.Execute(ctx, arguments)
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

type readFileTool struct{ ws *workspace.Workspace }

func (t *readFileTool) Definition() api.ToolDefinition {
	return api.ToolDefinition{
		Type: "function", Name: "read_file",
		Description: "Read a file inside the workspace. Text results use 1-based LINE_NUMBER→LINE_CONTENT formatting.",
		Parameters: objectSchema(map[string]any{
			"target_file": map[string]any{"type": "string", "description": "File path relative to the workspace."},
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
	path, err := t.ws.Resolve(requestedPath)
	if err != nil {
		return "", err
	}
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open %q: %w", requestedPath, err)
	}
	defer file.Close()

	reader := io.LimitReader(file, maxReadBytes+1)
	data, err := io.ReadAll(reader)
	if err != nil {
		return "", fmt.Errorf("read %q: %w", requestedPath, err)
	}
	if len(data) > maxReadBytes {
		return "", fmt.Errorf("file %q exceeds %d bytes", requestedPath, maxReadBytes)
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
	return output.String(), nil
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
	mode := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	if err := atomicWrite(path, []byte(args.Content), mode); err != nil {
		return "", err
	}
	return fmt.Sprintf("wrote %d bytes to %s", len(args.Content), t.ws.Relative(path)), nil
}

type editFileTool struct {
	ws       *workspace.Workspace
	approver Approver
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
	count := strings.Count(string(data), args.OldText)
	if count == 0 {
		return "", errors.New("old_text was not found")
	}
	if !args.ReplaceAll && count != 1 {
		return "", fmt.Errorf("old_text occurs %d times; provide more context or set replace_all", count)
	}
	if err := t.approver.Approve(ctx, "edit_file", fmt.Sprintf("%s (%d replacement(s))", t.ws.Relative(path), count)); err != nil {
		return "", err
	}
	replacements := 1
	if args.ReplaceAll {
		replacements = -1
	}
	updated := strings.Replace(string(data), args.OldText, args.NewText, replacements)
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if err := atomicWrite(path, []byte(updated), info.Mode().Perm()); err != nil {
		return "", err
	}
	return fmt.Sprintf("edited %s (%d replacement(s))", t.ws.Relative(path), count), nil
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
	err := command.Run()
	if commandCtx.Err() != nil {
		return output.String(), fmt.Errorf("command timed out after %s", t.timeout)
	}
	if err != nil {
		return output.String(), fmt.Errorf("command failed: %w", err)
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
