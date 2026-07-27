package acp

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/lookcorner/go-cli/internal/remote"
)

func (s *Server) conversationsClient() (*remote.ConversationsClient, error) {
	if !remote.ConversationsLaneActive() {
		return nil, errors.New("chat session action requires the conversations lane (OIDC + chat feature)")
	}
	config := s.authSnapshot()
	return &remote.ConversationsClient{
		HTTP: config.HTTP, BaseURL: remote.ResolveConversationsBaseURL(),
		AuthPath: config.Path, AuthScope: config.Scope, TokenProvider: config.TokenProvider,
	}, nil
}

func (s *Server) renameChatConversation(incoming message, conversationID, title string) {
	client, err := s.conversationsClient()
	if err != nil {
		s.respondError(incoming.ID, -32602, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), conversationsListTimeout)
	defer cancel()
	err = client.UpdateConversation(ctx, conversationID, remote.UpdateConversationBody{Title: &title})
	if err != nil {
		s.respondChatConversationError(incoming, "rename", conversationID, err)
		return
	}
	if current := s.lookupSession(conversationID); current != nil {
		current.mu.Lock()
		current.title = title
		current.updated = time.Now().UTC()
		current.mu.Unlock()
		s.notify(conversationID, map[string]any{
			"sessionUpdate": "session_info_update", "title": title,
			"updatedAt": time.Now().UTC().Format(time.RFC3339),
		})
	}
	s.respond(incoming.ID, map[string]any{"success": true})
}

func (s *Server) deleteChatConversation(incoming message, conversationID string) {
	client, err := s.conversationsClient()
	if err != nil {
		s.respondError(incoming.ID, -32602, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), conversationsListTimeout)
	defer cancel()
	if err := client.SoftDeleteConversation(ctx, conversationID); err != nil {
		s.respondChatConversationError(incoming, "delete", conversationID, err)
		return
	}
	s.closeSession(conversationID)
	s.respond(incoming.ID, map[string]any{"success": true})
}

func (s *Server) respondChatConversationError(incoming message, action, conversationID string, err error) {
	if errors.Is(err, remote.ErrNoOAuth) {
		s.respondError(incoming.ID, -32602, fmt.Sprintf("chat session %s requires xAI OAuth credentials", action))
		return
	}
	var httpErr remote.HTTPError
	if errors.As(err, &httpErr) && httpErr.Status == 404 && action == "rename" {
		s.respondError(incoming.ID, -32602, fmt.Sprintf("conversation not found: %s", conversationID))
		return
	}
	s.respondError(incoming.ID, -32603, fmt.Sprintf("chat conversation %s failed: %v", action, err))
}
