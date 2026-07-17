package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/lookcorner/go-cli/internal/api"
	"github.com/lookcorner/go-cli/internal/workspace"
)

type listDirTool struct{ ws *workspace.Workspace }

const listDirMaxChars = 10_000

type dirSummary struct {
	total int
	byExt map[string]int
}

func (s *dirSummary) add(path string) {
	if s.byExt == nil {
		s.byExt = make(map[string]int)
	}
	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), ".")
	if ext == "" {
		ext = "no-ext"
	}
	s.total++
	s.byExt[ext]++
}

func (s dirSummary) String() string {
	type bucket struct {
		ext   string
		count int
	}
	buckets := make([]bucket, 0, len(s.byExt))
	for ext, count := range s.byExt {
		buckets = append(buckets, bucket{ext, count})
	}
	sort.Slice(buckets, func(i, j int) bool {
		return buckets[i].count > buckets[j].count || buckets[i].count == buckets[j].count && buckets[i].ext < buckets[j].ext
	})
	parts, shown := make([]string, 0, 3), 0
	for _, item := range buckets[:min(3, len(buckets))] {
		name := "*." + item.ext
		if item.ext == "no-ext" {
			name = "*no-ext"
		}
		parts = append(parts, fmt.Sprintf("%d %s", item.count, name))
		shown += item.count
	}
	suffix := ""
	if shown < s.total {
		suffix = ", ..."
	}
	word := "files"
	if s.total == 1 {
		word = "file"
	}
	return fmt.Sprintf("[%d %s in subtree: %s%s]", s.total, word, strings.Join(parts, ", "), suffix)
}

func (t *listDirTool) Definition() api.ToolDefinition {
	return api.ToolDefinition{
		Type: "function", Name: "list_dir",
		Description: "List a directory as a bounded tree. Hidden and Git-ignored entries are omitted; large subdirectories are summarized by file extension.",
		Parameters: objectSchema(map[string]any{
			"target_directory": map[string]any{"type": "string", "description": "Directory path relative to the workspace."},
		}, "target_directory"),
	}
}

func (t *listDirTool) Execute(_ context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		TargetDirectory string `json:"target_directory"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("decode list_dir arguments: %w", err)
	}
	root, err := t.ws.Resolve(args.TargetDirectory)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(root)
	if err != nil {
		return "", fmt.Errorf("read directory %q: %w", args.TargetDirectory, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%q is a file, not a directory", args.TargetDirectory)
	}
	type entry struct {
		path  string
		rel   string
		isDir bool
	}
	var entries []entry
	err = filepath.WalkDir(root, func(path string, item os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		if strings.HasPrefix(item.Name(), ".") {
			if item.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		entries = append(entries, entry{path: path, rel: filepath.ToSlash(rel), isDir: item.IsDir()})
		if len(entries) >= 100_000 {
			return errLimitReached
		}
		return nil
	})
	truncated := errors.Is(err, errLimitReached)
	if err != nil && !truncated {
		return "", fmt.Errorf("walk directory %q: %w", args.TargetDirectory, err)
	}
	paths := make([]string, len(entries))
	for index := range entries {
		paths[index] = entries[index].path
	}
	ignored := workspace.GitIgnored(workspace.GitRoot(t.ws.Root()), paths)
	visible := entries[:0]
	for _, item := range entries {
		if !ignored[item.path] {
			visible = append(visible, item)
		}
	}
	entries = visible
	sort.Slice(entries, func(i, j int) bool {
		left, right := strings.ToLower(entries[i].rel), strings.ToLower(entries[j].rel)
		if left == right {
			return entries[i].rel < entries[j].rel
		}
		return left < right
	})
	summaries := make(map[string]*dirSummary)
	subtreeChars := make(map[string]int)
	for _, item := range entries {
		lineChars := len(strings.Repeat("  ", strings.Count(item.rel, "/")+1) + "- " + filepath.Base(item.path) + "\n")
		if item.isDir {
			lineChars++
		}
		for parent := filepath.ToSlash(filepath.Dir(item.rel)); parent != "."; parent = filepath.ToSlash(filepath.Dir(parent)) {
			subtreeChars[parent] += lineChars
			if !item.isDir {
				summary := summaries[parent]
				if summary == nil {
					summary = &dirSummary{}
					summaries[parent] = summary
				}
				summary.add(item.path)
			}
		}
	}
	var output strings.Builder
	fmt.Fprintf(&output, "- %s/\n", filepath.ToSlash(root))
	skipPrefix := ""
	for _, item := range entries {
		if skipPrefix != "" && strings.HasPrefix(item.rel, skipPrefix) {
			continue
		}
		skipPrefix = ""
		line := strings.Repeat("  ", strings.Count(item.rel, "/")+1) + "- " + filepath.Base(item.path)
		if item.isDir {
			line += "/"
		}
		line += "\n"
		if summary := summaries[item.rel]; item.isDir && summary != nil && output.Len()+len(line)+subtreeChars[item.rel] > listDirMaxChars {
			summaryLine := strings.Repeat("  ", strings.Count(item.rel, "/")+2) + summary.String() + "\n"
			if output.Len()+len(line)+len(summaryLine) <= listDirMaxChars {
				output.WriteString(line)
				output.WriteString(summaryLine)
				skipPrefix = item.rel + "/"
				continue
			}
		}
		if output.Len()+len(line) > listDirMaxChars {
			truncated = true
			break
		}
		output.WriteString(line)
	}
	if truncated {
		output.WriteString("    ...\n\n    Note: this directory is too large to list fully. Try list_dir on a narrower path, or use grep / run_terminal_cmd.\n")
	}
	return strings.TrimRight(output.String(), "\n"), nil
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
