package acp

import (
	"encoding/json"
	"errors"
	"os"
	"strings"

	sessionlog "github.com/lookcorner/go-cli/internal/session"
	shareapp "github.com/lookcorner/go-cli/internal/share"
)

const sessionUpdatesChunkSize = 64

type sessionUpdateEnvelope = shareapp.Update

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
	return shareapp.Updates(path, sessionID)
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
