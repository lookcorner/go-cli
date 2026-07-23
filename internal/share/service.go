package share

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

	"github.com/lookcorner/go-cli/internal/api"
	"github.com/lookcorner/go-cli/internal/auth"
	"github.com/lookcorner/go-cli/internal/session"
	"github.com/lookcorner/go-cli/internal/version"
)

type ErrorKind uint8

const (
	Invalid ErrorKind = iota + 1
	Authentication
	NotFound
	Internal
)

type Error struct {
	Kind    ErrorKind
	Message string
}

func (e *Error) Error() string { return e.Message }

type Service struct {
	SessionDir    string
	AuthPath      string
	AuthScope     string
	HTTP          *http.Client
	TokenProvider api.TokenProvider
	Enabled       func() bool
	BackendURL    string
	WebURL        string
}

type remoteMessage struct {
	Content   string  `json:"content"`
	Timestamp *string `json:"timestamp,omitempty"`
}

func (s Service) Share(ctx context.Context, sessionID string) (string, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return "", &Error{Kind: Invalid, Message: "session_id is required"}
	}
	credential, err := auth.Load(s.AuthPath, s.AuthScope)
	if err != nil || credential.Key == "" || !credential.IsXAIAuth() {
		return "", &Error{Kind: Authentication, Message: "Share session is disabled. Run `gork login` to authenticate."}
	}
	if s.Enabled == nil || !s.Enabled() {
		return "", &Error{Kind: Invalid, Message: "Session sharing is not available for your account."}
	}
	if credential.IsZDRTeam() {
		return "", &Error{Kind: Invalid, Message: "Session sharing is disabled for your team's data retention policy"}
	}

	info, err := session.InfoByID(s.SessionDir, sessionID)
	if err != nil {
		return "", &Error{Kind: NotFound, Message: "Session not found"}
	}
	path, err := session.PathForID(s.SessionDir, sessionID)
	if err != nil {
		return "", &Error{Kind: Invalid, Message: err.Error()}
	}
	updates, err := Updates(path, sessionID)
	if err != nil {
		return "", &Error{Kind: Internal, Message: "Failed to load session: " + err.Error()}
	}
	if len(updates) == 0 {
		return "", &Error{Kind: Invalid, Message: "No messages to share yet"}
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
	messages := make([]remoteMessage, 0, len(updates))
	for _, update := range updates {
		content, marshalErr := json.Marshal(map[string]any{"method": update.Method, "params": update.Params})
		if marshalErr != nil {
			return "", &Error{Kind: Internal, Message: "Failed to export session: " + marshalErr.Error()}
		}
		timestamp := time.UnixMilli(update.Timestamp).UTC().Format(time.RFC3339Nano)
		messages = append(messages, remoteMessage{Content: string(content), Timestamp: &timestamp})
	}

	token := credential.Key
	request := func(method, path string, payload any) ([]byte, int, error) {
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
			req, requestErr := http.NewRequestWithContext(ctx, method, strings.TrimRight(s.backendURL(), "/")+path, reader)
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
			client := s.HTTP
			if client == nil {
				client = http.DefaultClient
			}
			return client.Do(req)
		}
		response, requestErr := send(token)
		if requestErr != nil {
			return nil, 0, requestErr
		}
		if response.StatusCode == http.StatusUnauthorized && s.TokenProvider != nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
			response.Body.Close()
			refreshed, refreshErr := s.TokenProvider(ctx, token)
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

	remoteSession := map[string]any{"cwd": info.CWD, "status": "active", "metadata": metadata}
	if info.Title != "" {
		remoteSession["title"] = info.Title
	}
	upsert := map[string]any{"session": remoteSession, "agentId": "gork-go"}
	if _, status, requestErr := request(http.MethodPut, "/sessions/"+sessionID, upsert); requestErr != nil || status < 200 || status >= 300 {
		return "", &Error{Kind: Internal, Message: httpError("upsert session", status, requestErr)}
	}
	dataPayload := map[string]any{"messages": messages, "metadata": metadata}
	if _, status, requestErr := request(http.MethodPost, "/sessions/"+sessionID+"/data", dataPayload); requestErr != nil || status < 200 || status >= 300 && status != http.StatusRequestEntityTooLarge {
		return "", &Error{Kind: Internal, Message: httpError("save session data", status, requestErr)}
	}
	shareData, status, requestErr := request(http.MethodPost, "/sessions/"+sessionID+"/share", nil)
	if requestErr != nil || status < 200 || status >= 300 {
		return "", &Error{Kind: Internal, Message: httpError("create share link", status, requestErr)}
	}
	var response struct {
		PermissionID string `json:"permission_id"`
	}
	if json.Unmarshal(shareData, &response) != nil || response.PermissionID == "" {
		return "", &Error{Kind: Internal, Message: "invalid share response"}
	}
	return strings.TrimRight(s.webURL(), "/") + "/build/share/" + response.PermissionID, nil
}

func (s Service) backendURL() string {
	if s.BackendURL != "" {
		return s.BackendURL
	}
	if value := os.Getenv("GROK_CODE_BACKEND_URL"); value != "" {
		return value
	}
	return "https://code.grok.com"
}

func (s Service) webURL() string {
	if s.WebURL != "" {
		return s.WebURL
	}
	if value := os.Getenv("GROK_CODE_WEB_URL"); value != "" {
		return value
	}
	return "https://grok.com"
}

func httpError(operation string, status int, err error) string {
	if err != nil {
		return operation + ": " + err.Error()
	}
	return fmt.Sprintf("%s failed: HTTP %d", operation, status)
}
