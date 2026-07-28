package acp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/lookcorner/go-cli/internal/remote"
	sessionlog "github.com/lookcorner/go-cli/internal/session"
)

const conversationsListPageSize = 100
const conversationsListTimeout = 8 * time.Second

type sessionListCursor struct {
	Boundary        *sessionListBoundary `json:"boundary,omitempty"`
	ConvPageToken   string               `json:"conv_page_token,omitempty"`
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
	kind         string
	starred      bool
	workspaces   []string
	updatedAt    time.Time
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
	if remote.ProcessChatModeEnabled() {
		req.Meta = forceKindChatMeta(req.Meta)
	}
	filters, _ := req.Meta["x.ai/facetFilters"].(map[string]any)
	includeBuild := facetAllows(filters["kind"], "build")
	includeChat := facetAllows(filters["kind"], "chat")
	lane := remote.ConversationsLaneActive()
	if !includeBuild && !(lane && includeChat) {
		s.respondUnifiedSessionList(incoming, nil, nil, conversationsPartialOff())
		return
	}

	rawQuery := strings.TrimSpace(query)
	localQuery := strings.ToLower(rawQuery)
	cursor := decodeSessionListCursor(req.Cursor)
	rows := make([]sessionListRow, 0)

	if includeBuild {
		summaries, err := sessionlog.Summaries(s.SessionDir, req.CWD, 0)
		if err != nil {
			s.respondError(incoming.ID, -32000, err.Error())
			return
		}
		for _, summary := range summaries {
			if localQuery != "" && !strings.Contains(strings.ToLower(summary.SessionSummary), localQuery) && !strings.Contains(strings.ToLower(summary.Info.ID), localQuery) {
				continue
			}
			if !facetAllows(filters["cwd"], summary.Info.CWD) || !afterSessionBoundary(summary.UpdatedAt, "build", summary.Info.ID, cursor.Boundary) {
				continue
			}
			rows = append(rows, newSessionListRow(summary))
		}
	}

	partial := conversationsPartialOff()
	workspaceFilter := facetStringValues(filters["workspace"])
	workspacePushdown := ""
	if len(workspaceFilter) == 1 {
		workspacePushdown = workspaceFilter[0]
	}
	if lane && includeChat && !cursor.ConvPageDrained {
		chatRows, nextToken, reason, err := s.fetchConversationRows(rawQuery, cursor.ConvPageToken, workspacePushdown)
		if err != nil {
			partial = conversationsPartialReason(reason)
		} else {
			for _, row := range chatRows {
				if !facetBoolAllows(filters["starred"], row.starred) {
					continue
				}
				if len(workspaceFilter) > 0 && !facetAnyAllows(workspaceFilter, row.workspaces) {
					continue
				}
				if !afterSessionBoundary(row.updatedAt, row.kind, row.SessionID, cursor.Boundary) {
					continue
				}
				rows = append(rows, row)
			}
			cursor.ConvPageToken = nextToken
			if nextToken == "" {
				cursor.ConvPageDrained = true
			}
		}
	}

	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].updatedAt.Equal(rows[j].updatedAt) {
			if rows[i].kind == rows[j].kind {
				return rows[i].SessionID < rows[j].SessionID
			}
			return rows[i].kind < rows[j].kind
		}
		return rows[i].updatedAt.After(rows[j].updatedAt)
	})
	var next *sessionListCursor
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		next = &sessionListCursor{
			Boundary:        &sessionListBoundary{UpdatedAt: last.UpdatedAt, Kind: last.kind, SessionID: last.SessionID},
			ConvPageToken:   cursor.ConvPageToken,
			ConvPageDrained: cursor.ConvPageDrained,
		}
	} else if lane && includeChat && cursor.ConvPageToken != "" && !cursor.ConvPageDrained {
		next = &sessionListCursor{ConvPageToken: cursor.ConvPageToken, ConvPageDrained: false}
	}
	s.respondUnifiedSessionList(incoming, rows, next, partial)
}

func (s *Server) fetchConversationRows(query, pageToken, workspaceID string) ([]sessionListRow, string, string, error) {
	config := s.authSnapshot()
	client := &remote.ConversationsClient{
		HTTP: config.HTTP, BaseURL: remote.ResolveConversationsBaseURL(),
		AuthPath: config.Path, AuthScope: config.Scope, TokenProvider: config.TokenProvider,
	}
	ctx, cancel := context.WithTimeout(context.Background(), conversationsListTimeout)
	defer cancel()
	page, err := client.ListConversations(ctx, remote.ListQuery{
		PageSize: conversationsListPageSize, PageToken: pageToken, SearchQuery: query, WorkspaceID: workspaceID,
	})
	if err != nil {
		if errors.Is(err, remote.ErrNoOAuth) {
			return nil, "", "no_oauth", err
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, "", "timeout", err
		}
		return nil, "", "error", err
	}
	rows := make([]sessionListRow, 0, len(page.Conversations))
	for _, conversation := range page.Conversations {
		rows = append(rows, newConversationListRow(conversation))
	}
	return rows, page.NextPageToken, "", nil
}

func (s *Server) respondUnifiedSessionList(incoming message, rows []sessionListRow, next *sessionListCursor, partial map[string]any) {
	if rows == nil {
		rows = []sessionListRow{}
	}
	if partial == nil {
		partial = conversationsPartialOff()
	}
	result := map[string]any{
		"sessions": rows,
		"_meta": map[string]any{
			"x.ai/facets":  sessionListFacets(rows),
			"x.ai/partial": partial,
		},
	}
	if next != nil {
		result["nextCursor"] = encodeSessionListCursor(*next)
	}
	s.respond(incoming.ID, map[string]any{"result": result, "error": nil})
}

func conversationsPartialOff() map[string]any {
	return map[string]any{"conversations": false}
}

func conversationsPartialReason(reason string) map[string]any {
	return map[string]any{"conversations": true, "reason": reason}
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
		kind: "build", updatedAt: summary.UpdatedAt,
	}
}

func newConversationListRow(conversation remote.Conversation) sessionListRow {
	when := remote.ParseConversationTime(conversation)
	updated := conversation.ModifyTime
	if updated == "" {
		updated = conversation.CreateTime
	}
	if updated == "" && !when.IsZero() {
		updated = when.Format(time.RFC3339Nano)
	}
	created := conversation.CreateTime
	if created == "" {
		created = updated
	}
	var lastActive *string
	if conversation.ModifyTime != "" {
		value := conversation.ModifyTime
		lastActive = &value
	}
	title := conversation.Title
	workspaces := conversationWorkspaceIDs(conversation)
	facets := map[string]any{"kind": "chat"}
	if conversation.Starred {
		facets["starred"] = true
	}
	if len(workspaces) == 1 {
		facets["workspace"] = workspaces[0]
	} else if len(workspaces) > 1 {
		values := make([]any, 0, len(workspaces))
		for _, id := range workspaces {
			values = append(values, id)
		}
		facets["workspace"] = values
	}
	return sessionListRow{
		SessionID: conversation.ConversationID, Summary: title, UpdatedAt: updated, CreatedAt: created,
		CWD: "", Source: "conversation", NumMessages: 0, LastActiveAt: lastActive, Title: title,
		Meta: map[string]any{"x.ai/session": map[string]any{
			"kind": "chat", "facets": facets,
		}},
		kind: "chat", starred: conversation.Starred, workspaces: workspaces, updatedAt: when,
	}
}

func conversationWorkspaceIDs(conversation remote.Conversation) []string {
	ids := make([]string, 0, len(conversation.Workspaces))
	seen := map[string]struct{}{}
	for _, workspace := range conversation.Workspaces {
		id := strings.TrimSpace(workspace.WorkspaceID)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids
}

func sessionListFacets(rows []sessionListRow) map[string]any {
	kindCounts := map[string]int{}
	cwdCounts := make(map[string]int)
	workspaceCounts := make(map[string]int)
	starredCount := 0
	for _, row := range rows {
		kindCounts[row.kind]++
		if row.CWD != "" {
			cwdCounts[row.CWD]++
		}
		if row.starred {
			starredCount++
		}
		for _, workspace := range row.workspaces {
			workspaceCounts[workspace]++
		}
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
	kindValues := make([]map[string]any, 0, len(kindCounts))
	for _, kind := range []string{"build", "chat"} {
		if count := kindCounts[kind]; count > 0 {
			kindValues = append(kindValues, map[string]any{"value": kind, "count": count})
		}
	}
	if len(kindValues) > 0 {
		keys = append(keys, map[string]any{"key": "kind", "values": kindValues})
	}
	if starredCount > 0 {
		keys = append(keys, map[string]any{
			"key": "starred", "values": []map[string]any{{"value": true, "count": starredCount}},
		})
	}
	workspaces := make([]string, 0, len(workspaceCounts))
	for workspace := range workspaceCounts {
		workspaces = append(workspaces, workspace)
	}
	sort.Strings(workspaces)
	if len(workspaces) > 0 {
		values := make([]map[string]any, 0, len(workspaces))
		for _, workspace := range workspaces {
			values = append(values, map[string]any{"value": workspace, "count": workspaceCounts[workspace]})
		}
		keys = append(keys, map[string]any{"key": "workspace", "values": values})
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

func facetStringValues(raw any) []string {
	if raw == nil {
		return nil
	}
	switch value := raw.(type) {
	case string:
		if text := strings.TrimSpace(value); text != "" {
			return []string{text}
		}
	case []any:
		out := make([]string, 0, len(value))
		for _, item := range value {
			if text, ok := item.(string); ok {
				if text = strings.TrimSpace(text); text != "" {
					out = append(out, text)
				}
			}
		}
		return out
	}
	return nil
}

func facetAnyAllows(allowed, values []string) bool {
	if len(allowed) == 0 {
		return true
	}
	if len(values) == 0 {
		return false
	}
	set := map[string]struct{}{}
	for _, value := range values {
		set[value] = struct{}{}
	}
	for _, want := range allowed {
		if _, ok := set[want]; ok {
			return true
		}
	}
	return false
}

// facetBoolAllows matches Rust starred facet filtering. Build/local rows are
// partition-aware and skip this filter; only chat rows pass a bool value.
func facetBoolAllows(raw any, value bool) bool {
	if raw == nil {
		return true
	}
	wanted, ok := coerceFacetBool(raw)
	if !ok {
		return true
	}
	return wanted == value
}

func coerceFacetBool(raw any) (bool, bool) {
	switch value := raw.(type) {
	case bool:
		return value, true
	case string:
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "true", "1":
			return true, true
		case "false", "0":
			return false, true
		}
	case float64:
		if value == 1 {
			return true, true
		}
		if value == 0 {
			return false, true
		}
	case []any:
		if len(value) == 0 {
			return false, false
		}
		return coerceFacetBool(value[0])
	}
	return false, false
}

func forceKindChatMeta(meta map[string]any) map[string]any {
	if meta == nil {
		meta = map[string]any{}
	}
	filters, _ := meta["x.ai/facetFilters"].(map[string]any)
	if filters == nil {
		filters = map[string]any{}
	}
	filters["kind"] = []any{"chat"}
	meta["x.ai/facetFilters"] = filters
	return meta
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

func afterSessionBoundary(updatedAt time.Time, kind, sessionID string, boundary *sessionListBoundary) bool {
	if boundary == nil {
		return true
	}
	when, err := time.Parse(time.RFC3339Nano, boundary.UpdatedAt)
	if err != nil {
		when, err = time.Parse(time.RFC3339, boundary.UpdatedAt)
		if err != nil {
			return false
		}
	}
	if updatedAt.Before(when) {
		return true
	}
	if updatedAt.After(when) {
		return false
	}
	if kind != boundary.Kind {
		return kind > boundary.Kind
	}
	return sessionID > boundary.SessionID
}
