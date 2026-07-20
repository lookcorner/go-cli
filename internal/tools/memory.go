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
	if registry == nil || store == nil || !cfg.Enabled {
		return nil
	}
	if err := registry.Register(&memorySearchTool{store: store, index: cfg.Index, search: cfg.Search}); err != nil {
		return err
	}
	return registry.Register(&memoryGetTool{store: store})
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
		if warning := staleWarning(result); warning != "" {
			output.WriteString(warning + "\n")
		}
		fmt.Fprintf(&output, "```\n%s\n```\n", result.Snippet)
	}
	return strings.TrimSpace(output.String()), nil
}

func staleWarning(result memory.Result) string {
	if result.Source != "session" || result.CreatedAt <= 0 {
		return ""
	}
	age := time.Since(time.Unix(result.CreatedAt, 0))
	if age > 7*24*time.Hour {
		return "**Stale memory:** More than 7 days old; verify before relying on it."
	}
	if age > 24*time.Hour {
		return "**Verification recommended:** This session memory is more than 1 day old."
	}
	return ""
}

type memoryGetTool struct{ store *memory.Store }

func (t *memoryGetTool) Definition() api.ToolDefinition {
	return api.ToolDefinition{
		Type: "function", Name: "memory_get",
		Description: "Read a bounded line range from a global, workspace, or current-workspace session memory file.",
		Parameters: objectSchema(map[string]any{
			"path":  map[string]any{"type": "string", "description": "Absolute memory file path returned by memory_search."},
			"from":  map[string]any{"type": "integer", "minimum": 0, "description": "0-based starting line."},
			"lines": map[string]any{"type": "integer", "minimum": 0, "description": "Maximum number of lines; zero or omitted reads the remainder."},
		}, "path"),
	}
}

func (t *memoryGetTool) Execute(_ context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		Path  string `json:"path"`
		From  int    `json:"from"`
		Lines int    `json:"lines"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("decode memory_get arguments: %w", err)
	}
	file, err := t.store.Get(args.Path, args.From, args.Lines)
	if err != nil {
		return "", err
	}
	var output strings.Builder
	fmt.Fprintf(&output, "**File:** %s\n**Lines:** %d (from: %d, limit: %d)\n\n", file.Path, len(file.Lines), file.From, args.Lines)
	for index, line := range file.Lines {
		fmt.Fprintf(&output, "%d→%s\n", file.From+index+1, line)
	}
	return strings.TrimSuffix(output.String(), "\n"), nil
}
