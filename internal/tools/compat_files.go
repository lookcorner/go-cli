package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/lookcorner/go-cli/internal/api"
	"github.com/lookcorner/go-cli/internal/workspace"
)

type listDirTool struct{ ws *workspace.Workspace }

func (t *listDirTool) Definition() api.ToolDefinition {
	return api.ToolDefinition{
		Type: "function", Name: "list_dir",
		Description: "List files and directories below a workspace directory, excluding repository metadata.",
		Parameters: objectSchema(map[string]any{
			"target_directory": map[string]any{"type": "string", "description": "Directory path relative to the workspace."},
		}, "target_directory"),
	}
}

func (t *listDirTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		TargetDirectory string `json:"target_directory"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("decode list_dir arguments: %w", err)
	}
	encoded, _ := json.Marshal(map[string]string{"path": args.TargetDirectory})
	return (&listFilesTool{ws: t.ws}).Execute(ctx, encoded)
}

type grepTool struct{ ws *workspace.Workspace }

func (t *grepTool) Definition() api.ToolDefinition {
	integer := func(description string) map[string]any {
		return map[string]any{"type": "integer", "minimum": 0, "description": description}
	}
	return api.ToolDefinition{
		Type: "function", Name: "grep",
		Description: "Search file contents with ripgrep-compatible regular expressions and filters.",
		Parameters: objectSchema(map[string]any{
			"pattern":    map[string]any{"type": "string"},
			"path":       map[string]any{"type": "string"},
			"glob":       map[string]any{"type": "string"},
			"-B":         integer("Lines before each match."),
			"-A":         integer("Lines after each match."),
			"-C":         integer("Lines before and after each match."),
			"-i":         map[string]any{"type": "boolean"},
			"type":       map[string]any{"type": "string"},
			"head_limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 2000},
			"multiline":  map[string]any{"type": "boolean"},
		}, "pattern"),
	}
}

func (t *grepTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		Pattern         string `json:"pattern"`
		Path            string `json:"path"`
		Glob            string `json:"glob"`
		Before          int    `json:"-B"`
		After           int    `json:"-A"`
		Context         int    `json:"-C"`
		CaseInsensitive bool   `json:"-i"`
		Type            string `json:"type"`
		HeadLimit       int    `json:"head_limit"`
		Multiline       bool   `json:"multiline"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("decode grep arguments: %w", err)
	}
	root, err := t.ws.Resolve(args.Path)
	if err != nil {
		return "", err
	}
	commandArgs := []string{"--line-number", "--with-filename", "--color", "never", "--regexp", args.Pattern}
	if args.Glob != "" {
		commandArgs = append(commandArgs, "--glob", args.Glob)
	}
	if args.Before > 0 {
		commandArgs = append(commandArgs, "-B", strconv.Itoa(args.Before))
	}
	if args.After > 0 {
		commandArgs = append(commandArgs, "-A", strconv.Itoa(args.After))
	}
	if args.Context > 0 {
		commandArgs = append(commandArgs, "-C", strconv.Itoa(args.Context))
	}
	if args.CaseInsensitive {
		commandArgs = append(commandArgs, "-i")
	}
	if args.Type != "" {
		commandArgs = append(commandArgs, "--type", args.Type)
	}
	if args.Multiline {
		commandArgs = append(commandArgs, "-U", "--multiline-dotall")
	}
	commandArgs = append(commandArgs, root)
	cmd := exec.CommandContext(ctx, "rg", commandArgs...)
	cmd.Dir = t.ws.Root()
	var output cappedBuffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	err = cmd.Run()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return "no matches", nil
		}
		return "", fmt.Errorf("run grep: %w: %s", err, strings.TrimSpace(output.String()))
	}
	lines := strings.Split(strings.TrimRight(output.String(), "\n"), "\n")
	limit := args.HeadLimit
	if limit < 1 {
		limit = 200
	}
	if len(lines) > limit {
		lines = append(lines[:limit], fmt.Sprintf("[output truncated after %d lines]", limit))
	}
	result := strings.Join(lines, "\n")
	if len(result) > 40<<10 {
		result = result[:40<<10] + "\n[output truncated]"
	}
	return result, nil
}

type searchReplaceTool struct {
	ws       *workspace.Workspace
	approver Approver
}

func (t *searchReplaceTool) Definition() api.ToolDefinition {
	return api.ToolDefinition{
		Type: "function", Name: "search_replace",
		Description: "Replace an exact string in a file. An empty old_string creates or overwrites the file.",
		Parameters: objectSchema(map[string]any{
			"file_path":   map[string]any{"type": "string"},
			"old_string":  map[string]any{"type": "string"},
			"new_string":  map[string]any{"type": "string"},
			"replace_all": map[string]any{"type": "boolean"},
		}, "file_path", "old_string", "new_string"),
	}
}

func (t *searchReplaceTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		FilePath   string `json:"file_path"`
		OldString  string `json:"old_string"`
		NewString  string `json:"new_string"`
		ReplaceAll bool   `json:"replace_all"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("decode search_replace arguments: %w", err)
	}
	if args.OldString == args.NewString {
		return "", errors.New("old string and new string are the same")
	}
	if args.OldString == "" {
		encoded, _ := json.Marshal(map[string]string{"path": args.FilePath, "content": args.NewString})
		if _, err := (&writeFileTool{ws: t.ws, approver: t.approver}).Execute(ctx, encoded); err != nil {
			return "", err
		}
		return fmt.Sprintf("The file %s has been created successfully.", args.FilePath), nil
	}
	encoded, _ := json.Marshal(map[string]any{
		"path": args.FilePath, "old_text": args.OldString,
		"new_text": args.NewString, "replace_all": args.ReplaceAll,
	})
	return (&editFileTool{ws: t.ws, approver: t.approver}).Execute(ctx, encoded)
}
