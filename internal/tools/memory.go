package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/lookcorner/go-cli/internal/api"
	"github.com/lookcorner/go-cli/internal/memory"
)

func RegisterMemoryTools(registry *Registry, store *memory.Store, cfg memory.Config) error {
	return SetMemoryTools(registry, store, cfg, store != nil && cfg.Enabled)
}

func SetMemoryTools(registry *Registry, store *memory.Store, cfg memory.Config, enabled bool) error {
	if registry == nil {
		return nil
	}
	var replacements []Tool
	if enabled {
		if store == nil {
			return fmt.Errorf("memory store is required")
		}
		replacements = []Tool{&memorySearchTool{store: store, index: cfg.Index, search: cfg.Search}, &memoryGetTool{store: store}}
	}
	_, err := registry.Replace([]string{"memory_search", "memory_get"}, replacements)
	return err
}

func ParseMemoryCommand(prompt string) (string, bool) {
	fields := strings.Fields(strings.TrimSpace(prompt))
	if len(fields) == 0 || (fields[0] != "/memory" && fields[0] != "/mem") {
		return "", false
	}
	if len(fields) == 2 {
		switch strings.ToLower(fields[1]) {
		case "on", "enable":
			return "enable", true
		case "off", "disable":
			return "disable", true
		}
	}
	return "browse", true
}

func ParseRememberCommand(prompt string) (string, bool) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "/remember" {
		return "", true
	}
	if strings.HasPrefix(prompt, "/remember ") {
		return strings.TrimSpace(strings.TrimPrefix(prompt, "/remember")), true
	}
	return "", false
}

type memorySearchTool struct {
	store  *memory.Store
	index  memory.IndexConfig
	search memory.SearchConfig
}

func (t *memorySearchTool) Definition() api.ToolDefinition {
	return api.ToolDefinition{
		Type: "function", Name: "memory_search",
		Description: "Search cross-session memory for relevant knowledge chunks. Returns ranked results from global, workspace, and session memory files.",
		Parameters: objectSchema(map[string]any{
			"query":       map[string]any{"type": "string", "description": "Text to search for."},
			"max_results": map[string]any{"type": "integer", "minimum": 1},
			"min_score":   map[string]any{"type": "number", "minimum": 0, "maximum": 1},
		}, "query"),
	}
}

func (t *memorySearchTool) Execute(_ context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		Query      string   `json:"query"`
		MaxResults *int     `json:"max_results"`
		MinScore   *float64 `json:"min_score"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("decode memory_search arguments: %w", err)
	}
	search := t.search
	if args.MaxResults != nil {
		search.MaxResults = *args.MaxResults
	}
	if args.MinScore != nil {
		search.MinScore = *args.MinScore
	}
	results, err := t.store.Search(args.Query, t.index, search)
	if err != nil {
		return "", err
	}
	if len(results) == 0 {
		return "No memory results found for query.", nil
	}
	var output strings.Builder
	fmt.Fprintf(&output, "Found %d memory result(s):\n", len(results))
	for index, result := range results {
		fmt.Fprintf(&output, "\n### Result %d (score: %.2f, source: %s)\n**File:** %s (lines %d-%d)\n", index+1, result.Score, result.Source, result.Path, result.StartLine, result.EndLine)
		if warning := result.StalenessNote(time.Now()); warning != "" {
			output.WriteString(warning + "\n")
		}
		fmt.Fprintf(&output, "```\n%s\n```\n", result.Snippet)
	}
	return strings.TrimSpace(output.String()), nil
}

type memoryGetTool struct{ store *memory.Store }

func (t *memoryGetTool) Definition() api.ToolDefinition {
	return api.ToolDefinition{
		Type: "function", Name: "memory_get",
		Description: "Read a bounded line range from a global, workspace, or current-workspace session memory file.",
		Parameters: objectSchema(map[string]any{
			"path":  map[string]any{"type": "string", "description": "Absolute memory file path returned by memory_search."},
			"from":  map[string]any{"type": "integer", "minimum": 0, "description": "0-based start line (default: beginning of file)."},
			"lines": map[string]any{"type": "integer", "minimum": 0, "description": "Maximum number of lines to return (default: all)."},
		}, "path"),
	}
}

func (t *memoryGetTool) Execute(_ context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		Path  string `json:"path"`
		From  *int   `json:"from"`
		Lines *int   `json:"lines"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("decode memory_get arguments: %w", err)
	}
	from := 0
	if args.From != nil {
		from = *args.From
	}
	content, err := t.store.Get(args.Path, from, args.Lines)
	if err != nil {
		return "", err
	}
	fromLabel, linesLabel := "start", "all"
	if args.From != nil {
		fromLabel = fmt.Sprint(*args.From)
	}
	if args.Lines != nil {
		linesLabel = fmt.Sprint(*args.Lines)
	}
	var numbered []string
	if content != "" {
		numbered = strings.Split(content, "\n")
	}
	lineCount := len(numbered)
	if strings.HasSuffix(content, "\n") {
		lineCount--
	}
	formatted := make([]string, len(numbered))
	for index, line := range numbered {
		formatted[index] = fmt.Sprintf("%d→%s", from+index+1, line)
	}
	return fmt.Sprintf("**File:** %s\n**Lines:** %d (from: %s, limit: %s)\n\n%s", args.Path, lineCount, fromLabel, linesLabel, strings.Join(formatted, "\n")), nil
}
