package acp

import (
	"context"
	"errors"
	"fmt"
	"strings"
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

func sessionKindFromMeta(meta map[string]any) string {
	if meta == nil {
		return ""
	}
	sessionMeta, _ := meta["x.ai/session"].(map[string]any)
	if sessionMeta == nil {
		return ""
	}
	kind, _ := sessionMeta["kind"].(string)
	return strings.ToLower(strings.TrimSpace(kind))
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

func (s *Server) handleRestoreChatSession(ctx context.Context, incoming message, sessionID, cwd string, meta map[string]any) {
	if !remote.ConversationsLaneActive() {
		s.respondError(incoming.ID, -32602, "chat session load requires the conversations lane (OIDC + chat feature)")
		return
	}
	modes := s.fetchChatModesState(ctx)
	if existing := s.lookupSession(sessionID); existing != nil {
		existing.mu.Lock()
		if existing.kind == "" {
			existing.kind = "chat"
		}
		mode := existing.mode
		existing.mu.Unlock()
		s.respond(incoming.ID, s.chatSessionStartResponse(existing, mode, modes))
		return
	}

	model := ""
	if value, ok := meta["modelId"].(string); ok {
		model = strings.TrimSpace(value)
	}
	if model == "" {
		model = modes.CurrentModelID
	}
	yoloMode, autoMode := sessionPermissionModeOverrides(meta)
	config := SessionConfig{
		CWD: cwd, Model: model, SessionID: sessionID,
		DisplayCWD: stringMeta(meta, "x.ai/display_cwd"),
		YoloMode:   yoloMode, AutoMode: autoMode,
		ClientHooks: parseClientHooks(meta), HunkTrackerMode: s.clientHunkMode,
		// Chat profile: no client MCP / no local resume path (reference thin spawn).
	}
	created, err := s.startSession(ctx, sessionID, config, "")
	if err != nil {
		s.respondError(incoming.ID, -32000, err.Error())
		return
	}
	created.mu.Lock()
	created.kind = "chat"
	mode := created.mode
	created.mu.Unlock()
	s.respond(incoming.ID, s.chatSessionStartResponse(created, mode, modes))
	s.startFolderTrustPrompt(created)
}

func (s *Server) fetchChatModesState(ctx context.Context) remote.ModesModelState {
	config := s.authSnapshot()
	client := &remote.ChatModesClient{
		HTTP: config.HTTP, BaseURL: remote.ResolveModesBaseURL(),
		AuthPath: config.Path, AuthScope: config.Scope, TokenProvider: config.TokenProvider,
	}
	page, err := client.ListModesWithTimeout(ctx, "en", 2*time.Second)
	if err != nil {
		return remote.ModesModelState{}
	}
	return page.ToModelState()
}

func (s *Server) chatSessionStartResponse(current *session, mode string, modes remote.ModesModelState) map[string]any {
	response := sessionStartResponse(current, mode)
	meta := response["_meta"].(map[string]any)
	detail := meta["x.ai/sessionDetail"].(sessionDetail)
	detail.Kind = "chat"
	meta["x.ai/sessionDetail"] = detail
	meta["x.ai/session"] = map[string]any{"kind": "chat"}
	if len(modes.Available) > 0 {
		available := make([]modelInfo, 0, len(modes.Available))
		for _, item := range modes.Available {
			available = append(available, modelInfo{
				ModelID: item.ModelID, Name: item.Name, Description: item.Description, Meta: item.Meta,
			})
		}
		currentID := modes.CurrentModelID
		if currentID == "" {
			currentID = detail.CurrentModelID
		}
		response["models"] = sessionModelState{CurrentModelID: currentID, Available: available}
		detail.CurrentModelID = currentID
		meta["x.ai/sessionDetail"] = detail
	}
	return response
}
