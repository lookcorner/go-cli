package acp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/lookcorner/go-cli/internal/remote"
)

// chatHistoryPartialReason classifies why transcript replay stayed empty.
type chatHistoryPartialReason string

const (
	chatHistoryUnavailable chatHistoryPartialReason = "unavailable"
	chatHistoryNoOAuth     chatHistoryPartialReason = "no_oauth"
	chatHistoryTimeout     chatHistoryPartialReason = "timeout"
	chatHistoryError       chatHistoryPartialReason = "error"
)

func (s *Server) probeChatHistory(ctx context.Context, conversationID string) (remote.ListMessagesPage, chatHistoryPartialReason) {
	client, err := s.conversationsClient()
	if err != nil {
		return remote.ListMessagesPage{}, chatHistoryNoOAuth
	}
	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	page, err := client.ListMessages(probeCtx, conversationID, remote.ListMessagesQuery{PageSize: 50})
	if err == nil {
		return page, ""
	}
	if errors.Is(err, remote.ErrNoOAuth) {
		return remote.ListMessagesPage{}, chatHistoryNoOAuth
	}
	if errors.Is(err, remote.ErrMessagesUnavailable) {
		return remote.ListMessagesPage{}, chatHistoryUnavailable
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(probeCtx.Err(), context.DeadlineExceeded) {
		return remote.ListMessagesPage{}, chatHistoryTimeout
	}
	return remote.ListMessagesPage{}, chatHistoryError
}

func applyChatHistoryMeta(meta map[string]any, reason chatHistoryPartialReason, historyLoaded bool) {
	if meta == nil {
		return
	}
	sessionMeta, _ := meta["x.ai/session"].(map[string]any)
	if sessionMeta == nil {
		sessionMeta = map[string]any{"kind": "chat"}
	}
	sessionMeta["historyLoaded"] = historyLoaded
	meta["x.ai/session"] = sessionMeta
	if reason == "" {
		return
	}
	partial, _ := meta["x.ai/partial"].(map[string]any)
	if partial == nil {
		partial = map[string]any{}
	}
	partial["messages"] = true
	partial["reason"] = string(reason)
	meta["x.ai/partial"] = partial
}

func (s *Server) handleLoadChatHistory(incoming message) {
	var params struct {
		SessionID string `json:"sessionId"`
		BeforeID  string `json:"beforeId"`
		PageSize  int    `json:"pageSize"`
	}
	if err := json.Unmarshal(incoming.Params, &params); err != nil || strings.TrimSpace(params.SessionID) == "" {
		s.respondError(incoming.ID, -32602, "sessionId is required")
		return
	}
	client, err := s.conversationsClient()
	if err != nil {
		s.respond(incoming.ID, map[string]any{
			"messages": []any{}, "nextBeforeId": "",
			"_meta": map[string]any{
				"x.ai/partial": map[string]any{"messages": true, "reason": string(chatHistoryNoOAuth)},
				"x.ai/session": map[string]any{"kind": "chat", "historyLoaded": false},
			},
		})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), conversationsListTimeout)
	defer cancel()
	page, err := client.ListMessages(ctx, params.SessionID, remote.ListMessagesQuery{
		BeforeID: params.BeforeID, PageSize: params.PageSize,
	})
	if err != nil {
		reason := chatHistoryError
		switch {
		case errors.Is(err, remote.ErrNoOAuth):
			reason = chatHistoryNoOAuth
		case errors.Is(err, remote.ErrMessagesUnavailable):
			reason = chatHistoryUnavailable
		case errors.Is(err, context.DeadlineExceeded):
			reason = chatHistoryTimeout
		}
		s.respond(incoming.ID, map[string]any{
			"messages": []any{}, "nextBeforeId": "",
			"_meta": map[string]any{
				"x.ai/partial": map[string]any{"messages": true, "reason": string(reason)},
				"x.ai/session": map[string]any{"kind": "chat", "historyLoaded": false},
			},
		})
		return
	}
	messages := make([]map[string]any, 0, len(page.Messages))
	for _, item := range page.Messages {
		messages = append(messages, map[string]any{
			"messageId": item.MessageID, "role": item.Role, "content": item.Content,
			"createTime": item.CreateTime,
		})
	}
	s.respond(incoming.ID, map[string]any{
		"messages": messages, "nextBeforeId": page.NextBeforeID,
		"_meta": map[string]any{
			"x.ai/session": map[string]any{"kind": "chat", "historyLoaded": true},
		},
	})
}
