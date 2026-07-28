package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/lookcorner/go-cli/internal/api"
	"github.com/lookcorner/go-cli/internal/auth"
	"github.com/lookcorner/go-cli/internal/version"
)

const defaultConversationsBaseURL = "https://grok.com"

// ErrNoOAuth indicates the stored credential cannot call conversations APIs.
var ErrNoOAuth = errors.New("no OAuth credentials for conversations")

// HTTPError is a non-2xx conversations API response.
type HTTPError struct {
	Status int
}

func (e HTTPError) Error() string {
	return fmt.Sprintf("conversations request failed: HTTP %d", e.Status)
}

// Conversation is one grok.com app-chat conversation row.
type Conversation struct {
	ConversationID string      `json:"conversationId"`
	Title          string      `json:"title"`
	Starred        bool        `json:"starred"`
	CreateTime     string      `json:"createTime"`
	ModifyTime     string      `json:"modifyTime"`
	Workspaces     []Workspace `json:"workspaces"`
}

// Workspace is a grok.com workspace (list rows and conversation links).
type Workspace struct {
	WorkspaceID string `json:"workspaceId"`
	Name        string `json:"name,omitempty"`
	CreateTime  string `json:"createTime,omitempty"`
	Kind        string `json:"kind,omitempty"`
}

// ListQuery selects a page of conversations.
type ListQuery struct {
	PageSize    int
	PageToken   string
	SearchQuery string
	WorkspaceID string
}

// ListPage is one conversations list response.
type ListPage struct {
	Conversations []Conversation
	NextPageToken string
}

// UpdateConversationBody is PUT /rest/app-chat/conversations/{id}.
type UpdateConversationBody struct {
	Title   *string `json:"title,omitempty"`
	Starred *bool   `json:"starred,omitempty"`
}

// ConversationsClient talks to grok.com app-chat conversations.
type ConversationsClient struct {
	HTTP          *http.Client
	BaseURL       string
	AuthPath      string
	AuthScope     string
	TokenProvider api.TokenProvider
}

// ResolveConversationsBaseURL mirrors the Rust client env precedence.
func ResolveConversationsBaseURL() string {
	for _, key := range []string{"GROK_CONVERSATIONS_BASE_URL", "GROK_CODE_WEB_URL"} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return strings.TrimRight(value, "/")
		}
	}
	return defaultConversationsBaseURL
}

// ListConversations calls GET /rest/app-chat/conversations.
func (c *ConversationsClient) ListConversations(ctx context.Context, query ListQuery) (ListPage, error) {
	pageSize := query.PageSize
	if pageSize <= 0 {
		pageSize = 30
	}
	values := url.Values{}
	values.Set("pageSize", strconv.Itoa(pageSize))
	if token := strings.TrimSpace(query.PageToken); token != "" {
		values.Set("pageToken", token)
	}
	if search := strings.TrimSpace(query.SearchQuery); search != "" {
		values.Set("searchQuery", search)
	}
	if workspace := strings.TrimSpace(query.WorkspaceID); workspace != "" {
		values.Set("workspaceId", workspace)
	}
	data, err := c.do(ctx, http.MethodGet, "/rest/app-chat/conversations?"+values.Encode(), nil, false)
	if err != nil {
		return ListPage{}, err
	}
	var wire struct {
		Conversations     []Conversation `json:"conversations"`
		NextPageToken     string         `json:"nextPageToken"`
		TextSearchMatches []struct {
			Conversation *Conversation `json:"conversation"`
		} `json:"textSearchMatches"`
	}
	if json.Unmarshal(data, &wire) != nil {
		return ListPage{}, errors.New("failed to parse conversations response")
	}
	conversations := wire.Conversations
	if strings.TrimSpace(query.SearchQuery) != "" {
		conversations = conversations[:0]
		for _, match := range wire.TextSearchMatches {
			if match.Conversation != nil {
				conversations = append(conversations, *match.Conversation)
			}
		}
	}
	return ListPage{
		Conversations: conversations,
		NextPageToken: strings.TrimSpace(wire.NextPageToken),
	}, nil
}

// UpdateConversation calls PUT /rest/app-chat/conversations/{id}.
func (c *ConversationsClient) UpdateConversation(ctx context.Context, conversationID string, body UpdateConversationBody) error {
	id := strings.TrimSpace(conversationID)
	if id == "" {
		return errors.New("conversation id is required")
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	_, err = c.do(ctx, http.MethodPut, "/rest/app-chat/conversations/"+url.PathEscape(id), payload, false)
	return err
}

// SoftDeleteConversation calls DELETE /rest/app-chat/conversations/soft/{id}.
// HTTP 404 is treated as success for idempotent deletes.
func (c *ConversationsClient) SoftDeleteConversation(ctx context.Context, conversationID string) error {
	id := strings.TrimSpace(conversationID)
	if id == "" {
		return errors.New("conversation id is required")
	}
	_, err := c.do(ctx, http.MethodDelete, "/rest/app-chat/conversations/soft/"+url.PathEscape(id), nil, true)
	return err
}

// ErrMessagesUnavailable means grok.com has no messages history endpoint yet
// (or returned 404/405/501). Callers must fail closed and keep thin chat load.
var ErrMessagesUnavailable = errors.New("conversation messages API unavailable")

// ConversationMessage is one provisional app-chat transcript row.
// Wire names follow the conversations list style; confirm against grok-web before
// enabling real replay.
type ConversationMessage struct {
	MessageID  string `json:"messageId"`
	Role       string `json:"role"`
	Content    string `json:"content"`
	CreateTime string `json:"createTime,omitempty"`
}

// ListMessagesQuery pages older messages with a beforeId cursor.
type ListMessagesQuery struct {
	BeforeID string
	PageSize int
}

// ListMessagesPage is one conversation messages response.
type ListMessagesPage struct {
	Messages     []ConversationMessage
	NextBeforeID string
}

// ListMessages calls GET /rest/app-chat/conversations/{id}/messages (provisional).
// 404/405/501 map to ErrMessagesUnavailable so ACP can keep thin chat load.
func (c *ConversationsClient) ListMessages(ctx context.Context, conversationID string, query ListMessagesQuery) (ListMessagesPage, error) {
	id := strings.TrimSpace(conversationID)
	if id == "" {
		return ListMessagesPage{}, errors.New("conversation id is required")
	}
	pageSize := query.PageSize
	if pageSize <= 0 {
		pageSize = 50
	}
	values := url.Values{}
	values.Set("pageSize", strconv.Itoa(pageSize))
	if before := strings.TrimSpace(query.BeforeID); before != "" {
		values.Set("beforeId", before)
	}
	data, err := c.do(ctx, http.MethodGet, "/rest/app-chat/conversations/"+url.PathEscape(id)+"/messages?"+values.Encode(), nil, false)
	if err != nil {
		var httpErr HTTPError
		if errors.As(err, &httpErr) {
			switch httpErr.Status {
			case http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusNotImplemented:
				return ListMessagesPage{}, ErrMessagesUnavailable
			}
		}
		return ListMessagesPage{}, err
	}
	var wire struct {
		Messages     []ConversationMessage `json:"messages"`
		NextBeforeID string                `json:"nextBeforeId"`
	}
	if json.Unmarshal(data, &wire) != nil {
		return ListMessagesPage{}, errors.New("failed to parse conversation messages response")
	}
	return ListMessagesPage{
		Messages:     wire.Messages,
		NextBeforeID: strings.TrimSpace(wire.NextBeforeID),
	}, nil
}

func (c *ConversationsClient) do(ctx context.Context, method, path string, body []byte, acceptNotFound bool) ([]byte, error) {
	credential, err := auth.Load(c.AuthPath, c.AuthScope)
	if err != nil || credential.Key == "" || !credential.IsXAIAuth() {
		return nil, ErrNoOAuth
	}
	token := credential.Key
	if c.TokenProvider != nil {
		token, err = c.TokenProvider(ctx, "")
		if err != nil || token == "" {
			return nil, ErrNoOAuth
		}
	}
	base := strings.TrimRight(c.BaseURL, "/")
	if base == "" {
		base = ResolveConversationsBaseURL()
	}
	endpoint := base + path

	request := func(accessToken string) (*http.Response, error) {
		var reader io.Reader
		if body != nil {
			reader = bytes.NewReader(body)
		}
		req, requestErr := http.NewRequestWithContext(ctx, method, endpoint, reader)
		if requestErr != nil {
			return nil, requestErr
		}
		req.Header.Set("Authorization", "Bearer "+accessToken)
		req.Header.Set("X-XAI-Token-Auth", auth.DefaultTokenHeader)
		req.Header.Set("x-userid", credential.UserID)
		req.Header.Set("x-grok-client-version", version.Current)
		req.Header.Set("x-grok-client-identifier", "gork-go")
		req.Header.Set("x-grok-client-mode", "interactive")
		req.Header.Set("Accept", "application/json")
		if credential.Email != "" {
			req.Header.Set("x-email", credential.Email)
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		client := c.HTTP
		if client == nil {
			client = http.DefaultClient
		}
		return client.Do(req)
	}

	response, err := request(token)
	if err != nil {
		return nil, err
	}
	if response.StatusCode == http.StatusUnauthorized && c.TokenProvider != nil {
		io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		response.Body.Close()
		token, err = c.TokenProvider(ctx, token)
		if err != nil || token == "" {
			return nil, ErrNoOAuth
		}
		response, err = request(token)
		if err != nil {
			return nil, err
		}
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if response.StatusCode == http.StatusNotFound && acceptNotFound {
		return data, nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, HTTPError{Status: response.StatusCode}
	}
	return data, nil
}

// ParseConversationTime prefers modifyTime, then createTime.
func ParseConversationTime(c Conversation) time.Time {
	for _, raw := range []string{c.ModifyTime, c.CreateTime} {
		if when, err := parseFlexibleTime(raw); err == nil {
			return when
		}
	}
	return time.Time{}
}

func parseFlexibleTime(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, errors.New("empty time")
	}
	if when, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return when, nil
	}
	return time.Parse(time.RFC3339, raw)
}
