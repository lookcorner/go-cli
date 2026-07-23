package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/lookcorner/go-cli/internal/auth"
	sessionlog "github.com/lookcorner/go-cli/internal/session"
	"github.com/lookcorner/go-cli/internal/version"
)

type shareRequest struct {
	SessionID string `json:"session_id"`
}

type shareMessage struct {
	Content   string  `json:"content"`
	Timestamp *string `json:"timestamp,omitempty"`
}

func (s *Server) handleShareSession(ctx context.Context, incoming message) {
	var request shareRequest
	if json.Unmarshal(incoming.Params, &request) != nil || strings.TrimSpace(request.SessionID) == "" {
		s.respondErrorData(incoming.ID, -32602, "Invalid params", "session_id is required")
		return
	}
	config := s.authSnapshot()
	credential, err := auth.Load(config.Path, config.Scope)
	if err != nil || credential.Key == "" {
		s.respondErrorData(incoming.ID, -32000, "Authentication required", "Share session is disabled. Run `gork login` to authenticate.")
		return
	}
	if !credential.IsXAIAuth() {
		s.respondErrorData(incoming.ID, -32000, "Authentication required", "Share session is disabled. Run `gork login` to authenticate.")
		return
	}
	if s.SharingEnabled == nil || !s.SharingEnabled() {
		s.respondErrorData(incoming.ID, -32602, "Invalid params", "Session sharing is not available for your account.")
		return
	}
	if credential.IsZDRTeam() {
		s.respondErrorData(incoming.ID, -32602, "Invalid params", "Session sharing is disabled for your team's data retention policy")
		return
	}

	info, err := sessionlog.InfoByID(s.SessionDir, request.SessionID)
	if err != nil {
		s.respondErrorData(incoming.ID, -32002, "Session not found", "Session not found")
		return
	}
	path, err := sessionlog.PathForID(s.SessionDir, request.SessionID)
	if err != nil {
		s.respondErrorData(incoming.ID, -32602, "Invalid params", err.Error())
		return
	}
	updates, err := sessionUpdateEnvelopes(path, request.SessionID)
	if err != nil {
		s.respondErrorData(incoming.ID, -32603, "Internal error", "Failed to load session: "+err.Error())
		return
	}
	if len(updates) == 0 {
		s.respondErrorData(incoming.ID, -32602, "Invalid params", "No messages to share yet")
		return
	}
	metadata := map[string]any{
		"cwd": info.CWD, "total_messages": len(updates), "updated_at": info.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
	if updates[0].Timestamp > 0 {
		metadata["created_at"] = time.UnixMilli(updates[0].Timestamp).UTC().Format(time.RFC3339Nano)
	}
	if info.Title != "" {
		metadata["title"] = info.Title
	}
	if info.ModelID != "" {
		metadata["model_id"] = info.ModelID
	}
	messages := make([]shareMessage, 0, len(updates))
	for _, update := range updates {
		content, marshalErr := json.Marshal(map[string]any{"method": update.Method, "params": update.Params})
		if marshalErr != nil {
			s.respondErrorData(incoming.ID, -32603, "Internal error", "Failed to export session: "+marshalErr.Error())
			return
		}
		timestamp := time.UnixMilli(update.Timestamp).UTC().Format(time.RFC3339Nano)
		messages = append(messages, shareMessage{Content: string(content), Timestamp: &timestamp})
	}

	token := credential.Key
	requestHTTP := func(method, path string, payload any) ([]byte, int, error) {
		body, marshalErr := json.Marshal(payload)
		if payload == nil {
			body = nil
		}
		if marshalErr != nil {
			return nil, 0, marshalErr
		}
		send := func(currentToken string) (*http.Response, error) {
			var reader io.Reader
			if body != nil {
				reader = bytes.NewReader(body)
			}
			base := os.Getenv("GROK_CODE_BACKEND_URL")
			if base == "" {
				base = "https://code.grok.com"
			}
			req, requestErr := http.NewRequestWithContext(ctx, method, strings.TrimRight(base, "/")+path, reader)
			if requestErr != nil {
				return nil, requestErr
			}
			req.Header.Set("Authorization", "Bearer "+currentToken)
			req.Header.Set("X-XAI-Token-Auth", auth.DefaultTokenHeader)
			req.Header.Set("x-userid", credential.UserID)
			req.Header.Set("x-grok-client-version", version.Current)
			req.Header.Set("x-grok-client-mode", "interactive")
			req.Header.Set("x-grok-client-identifier", "gork-go")
			if credential.Email != "" {
				req.Header.Set("x-email", credential.Email)
			}
			if body != nil {
				req.Header.Set("Content-Type", "application/json")
			}
			client := config.HTTP
			if client == nil {
				client = http.DefaultClient
			}
			return client.Do(req)
		}
		response, requestErr := send(token)
		if requestErr != nil {
			return nil, 0, requestErr
		}
		if response.StatusCode == http.StatusUnauthorized && config.TokenProvider != nil {
			io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
			response.Body.Close()
			refreshed, refreshErr := config.TokenProvider(ctx, token)
			if refreshErr != nil || refreshed == "" {
				return nil, http.StatusUnauthorized, fmt.Errorf("authentication refresh failed")
			}
			token = refreshed
			response, requestErr = send(token)
			if requestErr != nil {
				return nil, 0, requestErr
			}
		}
		defer response.Body.Close()
		data, readErr := io.ReadAll(io.LimitReader(response.Body, 8<<20))
		return data, response.StatusCode, readErr
	}

	session := map[string]any{"cwd": info.CWD, "status": "active", "metadata": metadata}
	if info.Title != "" {
		session["title"] = info.Title
	}
	upsert := map[string]any{"session": session, "agentId": "gork-go"}
	if _, status, requestErr := requestHTTP(http.MethodPut, "/sessions/"+request.SessionID, upsert); requestErr != nil || status < 200 || status >= 300 {
		s.respondErrorData(incoming.ID, -32603, "Internal error", shareHTTPError("upsert session", status, requestErr))
		return
	}
	dataPayload := map[string]any{"messages": messages, "metadata": metadata}
	data, status, requestErr := requestHTTP(http.MethodPost, "/sessions/"+request.SessionID+"/data", dataPayload)
	if requestErr != nil || status < 200 || status >= 300 && status != http.StatusRequestEntityTooLarge {
		s.respondErrorData(incoming.ID, -32603, "Internal error", shareHTTPError("save session data", status, requestErr))
		return
	}
	_ = data
	shareData, status, requestErr := requestHTTP(http.MethodPost, "/sessions/"+request.SessionID+"/share", nil)
	if requestErr != nil || status < 200 || status >= 300 {
		s.respondErrorData(incoming.ID, -32603, "Internal error", shareHTTPError("create share link", status, requestErr))
		return
	}
	var response struct {
		PermissionID string `json:"permission_id"`
	}
	if json.Unmarshal(shareData, &response) != nil || response.PermissionID == "" {
		s.respondErrorData(incoming.ID, -32603, "Internal error", "invalid share response")
		return
	}
	webURL := os.Getenv("GROK_CODE_WEB_URL")
	if webURL == "" {
		webURL = "https://grok.com"
	}
	s.respond(incoming.ID, map[string]any{"share_url": strings.TrimRight(webURL, "/") + "/build/share/" + response.PermissionID})
}

func shareHTTPError(operation string, status int, err error) string {
	if err != nil {
		return operation + ": " + err.Error()
	}
	return fmt.Sprintf("%s failed: HTTP %d", operation, status)
}
