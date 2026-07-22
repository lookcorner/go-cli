package suggest

import (
	"context"
	"runtime"
	"sort"
	"strings"
)

const (
	maxHistory = 10
	maxPath    = 10
)

type Request struct {
	Text       string
	Cursor     int
	CWD        string
	Limit      int
	Generation uint64
	IncludeAI  bool
	AIModel    string
	TokenOnly  bool
}

type Ghost struct {
	FullText string `json:"fullText"`
	Suffix   string `json:"suffix"`
	Source   string `json:"source"`
}

type Completion struct {
	Display      string `json:"display"`
	Description  string `json:"description"`
	InsertText   string `json:"insertText"`
	Source       string `json:"source"`
	Priority     int    `json:"priority"`
	ReplaceRange []int  `json:"replaceRange,omitempty"`
	TokenText    string `json:"tokenText,omitempty"`
	Truncated    bool   `json:"truncated,omitempty"`
	ghost        bool
}

type Response struct {
	Ghost       *Ghost       `json:"ghost"`
	Completions []Completion `json:"completions"`
	Generation  uint64       `json:"generation"`
}

type AICompleter func(context.Context, string, string, string) (string, error)

func Generate(ctx context.Context, req Request, history []string, ai AICompleter) Response {
	req.Cursor = clampCursor(req.Text, req.Cursor)
	prefix := req.Text[:req.Cursor]
	var historyRows []Completion
	if !req.TokenOnly {
		historyRows = historySuggestions(prefix, req.Text, history)
	}

	var pathRows, fileRows []Completion
	if runtime.GOOS != "windows" {
		pathRows = pathSuggestions(prefix, req.Text)
		fileRows = fileSuggestions(prefix, req.Text, req.CWD)
	}

	rows := append(append(append([]Completion{}, historyRows...), pathRows...), fileRows...)
	if req.IncludeAI && !req.TokenOnly && ai != nil && !skipAI(historyRows, prefix) && prefix != "" {
		if raw, err := ai(ctx, prefix, req.CWD, req.AIModel); err == nil {
			if row, ok := aiSuggestion(prefix, req.Text, raw); ok {
				rows = append(rows, row)
			}
		}
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Priority > rows[j].Priority })

	response := Response{Generation: req.Generation, Completions: []Completion{}}
	for _, row := range rows {
		if row.ghost && response.Ghost == nil {
			suffix := row.InsertText
			if strings.HasPrefix(suffix, prefix) {
				suffix = strings.TrimPrefix(suffix, prefix)
			}
			response.Ghost = &Ghost{FullText: row.InsertText, Suffix: suffix, Source: row.Source}
		}
	}
	if req.Limit > 0 {
		rows = rows[:min(req.Limit, len(rows))]
	} else {
		rows = []Completion{}
	}
	response.Completions = rows
	return response
}

func clampCursor(text string, cursor int) int {
	if cursor < 0 {
		return 0
	}
	if cursor > len(text) {
		cursor = len(text)
	}
	for cursor > 0 && !isRuneBoundary(text, cursor) {
		cursor--
	}
	return cursor
}

func isRuneBoundary(text string, index int) bool {
	return index == 0 || index == len(text) || text[index]&0xc0 != 0x80
}

func historySuggestions(prefix, text string, history []string) []Completion {
	if prefix == "" {
		return nil
	}
	seen := map[string]bool{}
	rows := make([]Completion, 0, maxHistory)
	for _, value := range history {
		if !strings.HasPrefix(value, prefix) || seen[value] {
			continue
		}
		seen[value] = true
		priority := max(0, 10-len(rows))
		if value == prefix {
			priority += 30
		}
		rows = append(rows, Completion{Display: value, InsertText: value, Source: "history", Priority: priority, ReplaceRange: []int{0, len(text)}, ghost: len(rows) == 0})
		if len(rows) == maxHistory {
			break
		}
	}
	return rows
}

func skipAI(history []Completion, prefix string) bool {
	return len(history) > 0 && (history[0].Priority >= 30 || prefix != "" && len(history) >= 3)
}

func aiSuggestion(prefix, text, raw string) (Completion, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed == prefix {
		return Completion{}, false
	}
	completed := trimmed
	if !strings.HasPrefix(trimmed, prefix) {
		completed = strings.TrimRight(prefix+raw, " \t\r\n")
	}
	return Completion{Display: completed, InsertText: completed, Source: "ai", Priority: -10, ReplaceRange: []int{0, len(text)}, ghost: true}, true
}
