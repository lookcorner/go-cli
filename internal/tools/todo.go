package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/lookcorner/go-cli/internal/api"
)

type todoItem struct {
	id      string
	content string
	status  string
}

type todoStore struct {
	mu    sync.Mutex
	items []todoItem
}

func newTodoStore() *todoStore { return &todoStore{} }

type todoWriteTool struct{ store *todoStore }

func (t *todoWriteTool) Definition() api.ToolDefinition {
	return api.ToolDefinition{
		Type: "function", Name: "todo_write",
		Description: "Create and manage a structured task list. Use for tasks with three or more steps; merge partial status updates by id.",
		Parameters: objectSchema(map[string]any{
			"merge": map[string]any{"type": "boolean", "description": "Merge by id (default true); false replaces the whole list."},
			"todos": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"id":      map[string]any{"type": "string"},
						"content": map[string]any{"type": "string"},
						"status":  map[string]any{"type": "string", "enum": []string{"pending", "in_progress", "completed", "cancelled"}},
					},
					"required": []string{"id"}, "additionalProperties": false,
				},
			},
		}, "todos"),
	}
}

func (t *todoWriteTool) Execute(_ context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		Merge *bool `json:"merge"`
		Todos []struct {
			ID      string  `json:"id"`
			Content *string `json:"content"`
			Status  *string `json:"status"`
		} `json:"todos"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("decode todo_write arguments: %w", err)
	}
	seen := make(map[string]struct{}, len(args.Todos))
	for _, update := range args.Todos {
		if strings.TrimSpace(update.ID) == "" {
			return "", errors.New("todo id must not be empty")
		}
		if _, exists := seen[update.ID]; exists {
			return fmt.Sprintf("Duplicate todo ID in request: %q. Each todo item must have a unique ID.", update.ID), nil
		}
		seen[update.ID] = struct{}{}
		if update.Status != nil && !validTodoStatus(*update.Status) {
			return "", fmt.Errorf("invalid todo status %q", *update.Status)
		}
	}
	merge := args.Merge == nil || *args.Merge
	t.store.mu.Lock()
	defer t.store.mu.Unlock()
	if !merge && len(t.store.items) > 0 && len(args.Todos) > 0 {
		merge = true
		for _, update := range args.Todos {
			if update.Content != nil && *update.Content != "" {
				merge = false
				break
			}
			found := false
			for _, item := range t.store.items {
				if item.id == update.ID {
					found = true
					break
				}
			}
			if !found {
				merge = false
				break
			}
		}
	}
	if !merge {
		t.store.items = nil
	}
	for _, update := range args.Todos {
		index := -1
		for i := range t.store.items {
			if t.store.items[i].id == update.ID {
				index = i
				break
			}
		}
		if index >= 0 {
			if update.Content != nil && *update.Content != "" {
				t.store.items[index].content = *update.Content
			}
			if update.Status != nil {
				t.store.items[index].status = *update.Status
			}
			continue
		}
		content := update.ID
		if update.Content != nil && *update.Content != "" {
			content = *update.Content
		}
		status := "pending"
		if update.Status != nil {
			status = *update.Status
		}
		t.store.items = append(t.store.items, todoItem{id: update.ID, content: content, status: status})
	}
	if len(t.store.items) == 0 {
		return "No tasks currently tracked.", nil
	}
	var output strings.Builder
	for _, item := range t.store.items {
		fmt.Fprintf(&output, "- [%s] %s: %s\n", item.status, item.id, item.content)
	}
	return output.String(), nil
}

func validTodoStatus(status string) bool {
	switch status {
	case "pending", "in_progress", "completed", "cancelled":
		return true
	default:
		return false
	}
}
