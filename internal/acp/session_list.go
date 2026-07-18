package acp

import (
	"encoding/base64"
	"encoding/json"
	"sort"
	"strings"
	"time"

	sessionlog "github.com/lookcorner/go-cli/internal/session"
)

type sessionListCursor struct {
	Boundary        *sessionListBoundary `json:"boundary,omitempty"`
	ConvPageDrained bool                 `json:"conv_page_drained"`
}

type sessionListBoundary struct {
	UpdatedAt string `json:"updated_at"`
	Kind      string `json:"kind"`
	SessionID string `json:"session_id"`
}

type sessionListRow struct {
	SessionID    string         `json:"sessionId"`
	Summary      string         `json:"summary"`
	UpdatedAt    string         `json:"updatedAt"`
	CreatedAt    string         `json:"createdAt"`
	CWD          string         `json:"cwd"`
	Source       string         `json:"source"`
	ModelID      *string        `json:"modelId,omitempty"`
	NumMessages  int            `json:"numMessages"`
	LastActiveAt *string        `json:"lastActiveAt,omitempty"`
	Title        string         `json:"title"`
	Meta         map[string]any `json:"_meta"`
	summary      sessionlog.Summary
}

func (s *Server) handleUnifiedSessionList(incoming message) {
	var req struct {
		CWD    string         `json:"cwd"`
		Query  string         `json:"query"`
		Limit  *int           `json:"limit"`
		Cursor string         `json:"cursor"`
		Meta   map[string]any `json:"_meta"`
	}
	if json.Unmarshal(incoming.Params, &req) != nil {
		s.respondError(incoming.ID, -32602, "invalid session list parameters")
		return
	}
	query, limit := req.Query, 30
	if value, ok := req.Meta["x.ai/query"].(string); query == "" && ok {
		query = value
	}
	if req.Limit != nil {
		if *req.Limit < 0 {
			s.respondError(incoming.ID, -32602, "limit must not be negative")
			return
		}
		if *req.Limit > 0 {
			limit = *req.Limit
		}
	} else if value, ok := numberAsInt(req.Meta["x.ai/limit"]); ok && value > 0 {
		limit = value
	}
	filters, _ := req.Meta["x.ai/facetFilters"].(map[string]any)
	if !facetAllows(filters["kind"], "build") {
		s.respondUnifiedSessionList(incoming, nil, nil)
		return
	}
	summaries, err := sessionlog.Summaries(s.SessionDir, req.CWD, 0)
	if err != nil {
		s.respondError(incoming.ID, -32000, err.Error())
		return
	}
	query = strings.ToLower(strings.TrimSpace(query))
	cursor := decodeSessionListCursor(req.Cursor)
	rows := make([]sessionListRow, 0, len(summaries))
	for _, summary := range summaries {
		if query != "" && !strings.Contains(strings.ToLower(summary.SessionSummary), query) && !strings.Contains(strings.ToLower(summary.Info.ID), query) {
			continue
		}
		if !facetAllows(filters["cwd"], summary.Info.CWD) || !afterSessionBoundary(summary, cursor.Boundary) {
			continue
		}
		rows = append(rows, newSessionListRow(summary))
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].summary.UpdatedAt.Equal(rows[j].summary.UpdatedAt) {
			return rows[i].SessionID < rows[j].SessionID
		}
		return rows[i].summary.UpdatedAt.After(rows[j].summary.UpdatedAt)
	})
	var next *sessionListCursor
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		next = &sessionListCursor{Boundary: &sessionListBoundary{UpdatedAt: last.UpdatedAt, Kind: "build", SessionID: last.SessionID}}
	}
	s.respondUnifiedSessionList(incoming, rows, next)
}

func (s *Server) respondUnifiedSessionList(incoming message, rows []sessionListRow, next *sessionListCursor) {
	if rows == nil {
		rows = []sessionListRow{}
	}
	result := map[string]any{
		"sessions": rows,
		"_meta": map[string]any{
			"x.ai/facets":  sessionListFacets(rows),
			"x.ai/partial": map[string]any{"conversations": false},
		},
	}
	if next != nil {
		result["nextCursor"] = encodeSessionListCursor(*next)
	}
	s.respond(incoming.ID, map[string]any{"result": result, "error": nil})
}

func newSessionListRow(summary sessionlog.Summary) sessionListRow {
	updated, created := summary.UpdatedAt.Format(time.RFC3339Nano), summary.CreatedAt.Format(time.RFC3339Nano)
	var lastActive *string
	if summary.LastActiveAt != nil {
		value := summary.LastActiveAt.Format(time.RFC3339Nano)
		lastActive = &value
	}
	return sessionListRow{
		SessionID: summary.Info.ID, Summary: summary.SessionSummary, UpdatedAt: updated, CreatedAt: created,
		CWD: summary.Info.CWD, Source: "local", ModelID: optionalRosterString(summary.CurrentModelID),
		NumMessages: summary.NumMessages, LastActiveAt: lastActive, Title: summary.SessionSummary,
		Meta: map[string]any{"x.ai/session": map[string]any{
			"kind": "build", "facets": map[string]any{"kind": "build", "cwd": summary.Info.CWD},
		}},
		summary: summary,
	}
}

func sessionListFacets(rows []sessionListRow) map[string]any {
	kindCount := len(rows)
	cwdCounts := make(map[string]int)
	for _, row := range rows {
		cwdCounts[row.CWD]++
	}
	cwds := make([]string, 0, len(cwdCounts))
	for cwd := range cwdCounts {
		cwds = append(cwds, cwd)
	}
	sort.Strings(cwds)
	cwdValues := make([]map[string]any, 0, len(cwds))
	for _, cwd := range cwds {
		cwdValues = append(cwdValues, map[string]any{"value": cwd, "count": cwdCounts[cwd]})
	}
	keys := []map[string]any{}
	if len(cwdValues) > 0 {
		keys = append(keys, map[string]any{"key": "cwd", "values": cwdValues})
	}
	if kindCount > 0 {
		keys = append(keys, map[string]any{"key": "kind", "values": []map[string]any{{"value": "build", "count": kindCount}}})
	}
	return map[string]any{"scope": "window", "keys": keys}
}

func facetAllows(raw any, value string) bool {
	if raw == nil {
		return true
	}
	switch allowed := raw.(type) {
	case string:
		return allowed == value
	case []any:
		if len(allowed) == 0 {
			return true
		}
		for _, item := range allowed {
			if text, ok := item.(string); ok && text == value {
				return true
			}
		}
	}
	return false
}

func numberAsInt(value any) (int, bool) {
	number, ok := value.(float64)
	return int(number), ok && number == float64(int(number))
}

func decodeSessionListCursor(raw string) sessionListCursor {
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return sessionListCursor{}
	}
	var cursor sessionListCursor
	if json.Unmarshal(data, &cursor) != nil {
		return sessionListCursor{}
	}
	return cursor
}

func encodeSessionListCursor(cursor sessionListCursor) string {
	data, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(data)
}

func afterSessionBoundary(summary sessionlog.Summary, boundary *sessionListBoundary) bool {
	if boundary == nil {
		return true
	}
	when, err := time.Parse(time.RFC3339Nano, boundary.UpdatedAt)
	if err != nil {
		return false
	}
	return summary.UpdatedAt.Before(when) || (summary.UpdatedAt.Equal(when) && summary.Info.ID > boundary.SessionID)
}
