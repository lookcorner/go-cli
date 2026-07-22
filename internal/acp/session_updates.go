package acp

import (
	"encoding/json"
	"errors"
	"os"
	"strings"

	sessionlog "github.com/lookcorner/go-cli/internal/session"
)

const sessionUpdatesChunkSize = 64

type sessionUpdateEnvelope struct {
	Timestamp int64          `json:"timestamp"`
	Method    string         `json:"method"`
	Params    map[string]any `json:"params"`
}

type sessionUpdatesRequest struct {
	SessionID string         `json:"sessionId"`
	CWD       string         `json:"cwd"`
	Offset    *int64         `json:"offset"`
	Limit     *int           `json:"limit"`
	Stream    bool           `json:"stream"`
	ChunkSize *int           `json:"chunkSize"`
	TurnIndex *int           `json:"turnIndex"`
	Meta      map[string]any `json:"_meta"`
}

func (s *Server) handleSessionUpdates(incoming message) {
	var request sessionUpdatesRequest
	if json.Unmarshal(incoming.Params, &request) != nil || request.SessionID == "" || request.CWD == "" || request.Limit != nil && *request.Limit < 0 || request.ChunkSize != nil && *request.ChunkSize < 0 || request.TurnIndex != nil && *request.TurnIndex < 0 {
		s.respondError(incoming.ID, -32602, "sessionId, cwd, and non-negative pagination values are required")
		return
	}
	path, err := sessionlog.PathForID(s.SessionDir, request.SessionID)
	if err != nil {
		s.respondError(incoming.ID, -32602, err.Error())
		return
	}
	updates, err := sessionUpdateEnvelopes(path, request.SessionID)
	if errors.Is(err, os.ErrNotExist) {
		updates, err = []sessionUpdateEnvelope{}, nil
	}
	if err != nil {
		s.respondError(incoming.ID, -32000, err.Error())
		return
	}
	promptStarts := sessionPromptStarts(updates)
	start, end, hasMore := sessionUpdatePage(request, len(updates), promptStarts)
	page := updates[start:end]
	lastEventID := sessionUpdatesLastEventID(page)

	if request.Stream {
		chunkSize := sessionUpdatesChunkSize
		if request.ChunkSize != nil {
			chunkSize = max(1, *request.ChunkSize)
		}
		chunkCount := 0
		for index := 0; index < len(page); index += chunkSize {
			chunkEnd := min(index+chunkSize, len(page))
			params := map[string]any{
				"sessionId": request.SessionID,
				"index":     chunkCount,
				"updates":   page[index:chunkEnd],
				"done":      chunkEnd == len(page),
			}
			if len(request.Meta) > 0 {
				params["_meta"] = request.Meta
			}
			s.write(map[string]any{"jsonrpc": "2.0", "method": "x.ai/session/updates/chunk", "params": params})
			chunkCount++
		}
		result := map[string]any{"totalCount": len(updates), "chunkCount": chunkCount, "promptStarts": promptStarts}
		if lastEventID != "" {
			result["lastEventId"] = lastEventID
		}
		s.respond(incoming.ID, result)
		return
	}

	result := map[string]any{"updates": page, "totalCount": len(updates), "hasMore": hasMore, "promptStarts": promptStarts}
	if lastEventID != "" {
		result["lastEventId"] = lastEventID
	}
	s.respond(incoming.ID, result)
}

func sessionUpdateEnvelopes(path, sessionID string) ([]sessionUpdateEnvelope, error) {
	events, err := sessionlog.Events(path)
	if err != nil {
		return nil, err
	}
	updates := make([]sessionUpdateEnvelope, 0, len(events))
	appendStandard := func(timestamp int64, update map[string]any) {
		updates = append(updates, sessionUpdateEnvelope{Timestamp: timestamp, Method: "session/update", Params: map[string]any{"sessionId": sessionID, "update": update}})
	}
	appendExtension := func(timestamp int64, update map[string]any, meta map[string]any) {
		params := map[string]any{"sessionId": sessionID, "update": update}
		if len(meta) > 0 {
			params["_meta"] = meta
		}
		updates = append(updates, sessionUpdateEnvelope{Timestamp: timestamp, Method: "_x.ai/session/update", Params: params})
	}

	for _, event := range events {
		timestamp := event.Time.UnixMilli()
		switch event.Kind {
		case "user_prompt":
			var data struct {
				Text      string               `json:"text"`
				Content   []sessionlog.Content `json:"content"`
				Synthetic bool                 `json:"synthetic"`
			}
			if decodeSessionEvent(event.Data, &data) != nil || data.Synthetic {
				continue
			}
			if len(data.Content) == 0 {
				if data.Text != "" {
					appendStandard(timestamp, messageChunk("user_message_chunk", map[string]any{"type": "text", "text": data.Text}))
				}
				continue
			}
			hasText := false
			for _, part := range data.Content {
				hasText = hasText || part.Type == "text"
			}
			if data.Text != "" && !hasText {
				appendStandard(timestamp, messageChunk("user_message_chunk", map[string]any{"type": "text", "text": data.Text}))
			}
			for _, part := range data.Content {
				part, materializeErr := sessionlog.MaterializeContent(path, part)
				if materializeErr != nil {
					return nil, materializeErr
				}
				content := map[string]any{"type": part.Type}
				if part.Type == "text" {
					content["text"] = part.Text
				} else if part.Data == "" {
					content["uri"] = part.URI
				} else {
					content["data"], content["mimeType"] = part.Data, part.MimeType
				}
				appendStandard(timestamp, messageChunk("user_message_chunk", content))
			}
		case "model_response":
			var data struct {
				Text string `json:"text"`
			}
			if decodeSessionEvent(event.Data, &data) == nil && data.Text != "" {
				appendStandard(timestamp, messageChunk("agent_message_chunk", map[string]any{"type": "text", "text": data.Text}))
			}
		case "tool_call":
			var data struct {
				CallID    string          `json:"call_id"`
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			if decodeSessionEvent(event.Data, &data) == nil && data.CallID != "" {
				update := map[string]any{"sessionUpdate": "tool_call", "toolCallId": data.CallID, "title": data.Name, "kind": acpToolKind(data.Name), "status": "in_progress"}
				if len(data.Arguments) > 0 {
					update["rawInput"] = data.Arguments
				}
				appendStandard(timestamp, update)
			}
		case "tool_result":
			var data struct {
				CallID string `json:"call_id"`
				Output string `json:"output"`
				Failed bool   `json:"failed"`
			}
			if decodeSessionEvent(event.Data, &data) == nil && data.CallID != "" {
				status := "completed"
				if data.Failed {
					status = "failed"
				}
				appendStandard(timestamp, map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": data.CallID, "status": status, "rawOutput": data.Output})
			}
		case "session_mode":
			var data struct {
				ModeID string `json:"mode_id"`
			}
			if decodeSessionEvent(event.Data, &data) == nil && data.ModeID != "" {
				appendStandard(timestamp, map[string]any{"sessionUpdate": "current_mode_update", "currentModeId": data.ModeID})
			}
		case "subagent_spawned", "subagent_finished", "task_backgrounded", "task_completed":
			update, ok := event.Data.(map[string]any)
			if !ok {
				continue
			}
			update = cloneMap(update)
			if _, exists := update["sessionUpdate"]; !exists {
				update["sessionUpdate"] = event.Kind
			}
			appendExtension(timestamp, update, nil)
		case "xai_session_notification":
			params, ok := event.Data.(map[string]any)
			if !ok {
				continue
			}
			update, _ := params["update"].(map[string]any)
			meta, _ := params["_meta"].(map[string]any)
			if update != nil {
				appendExtension(timestamp, update, meta)
			}
		}
	}
	return updates, nil
}

func decodeSessionEvent(data any, target any) error {
	encoded, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return json.Unmarshal(encoded, target)
}

func messageChunk(kind string, content map[string]any) map[string]any {
	return map[string]any{"sessionUpdate": kind, "content": content}
}

func cloneMap(value map[string]any) map[string]any {
	cloned := make(map[string]any, len(value)+1)
	for key, item := range value {
		cloned[key] = item
	}
	return cloned
}

func sessionPromptStarts(updates []sessionUpdateEnvelope) []int {
	starts := []int{}
	inUser := false
	for index, envelope := range updates {
		update, _ := envelope.Params["update"].(map[string]any)
		isUser := update["sessionUpdate"] == "user_message_chunk"
		if isUser && !inUser {
			starts = append(starts, index)
		}
		inUser = isUser
	}
	return starts
}

func sessionUpdatePage(request sessionUpdatesRequest, total int, promptStarts []int) (start, end int, hasMore bool) {
	if request.Offset == nil && request.TurnIndex != nil && *request.TurnIndex > 0 {
		if *request.TurnIndex < len(promptStarts) {
			start = promptStarts[len(promptStarts)-*request.TurnIndex]
		}
		end = total
		if request.Limit != nil {
			end = min(start+*request.Limit, total)
		}
		return start, end, start > 0
	}
	if request.Offset != nil {
		if *request.Offset < 0 {
			if *request.Offset > -int64(total) {
				start = total + int(*request.Offset)
			}
		} else if *request.Offset < int64(total) {
			start = int(*request.Offset)
		} else {
			start = total
		}
	}
	end = total
	if request.Limit != nil {
		end = min(start+*request.Limit, total)
	}
	return start, end, end < total
}

func sessionUpdatesLastEventID(updates []sessionUpdateEnvelope) string {
	for index := len(updates) - 1; index >= 0; index-- {
		meta, _ := updates[index].Params["_meta"].(map[string]any)
		if value, _ := meta["eventId"].(string); strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
