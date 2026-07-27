package remote

import (
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

// ErrNoOAuth indicates the stored credential cannot call conversations:read.
var ErrNoOAuth = errors.New("no OAuth credentials for conversations:read")

// Conversation is one grok.com app-chat conversation row.
type Conversation struct {
	ConversationID string `json:"conversationId"`
	Title          string `json:"title"`
	Starred        bool   `json:"starred"`
	CreateTime     string `json:"createTime"`
	ModifyTime     string `json:"modifyTime"`
}

// ListQuery selects a page of conversations.
type ListQuery struct {
	PageSize     int
	PageToken    string
	SearchQuery  string
	WorkspaceID  string
}

// ListPage is one conversations list response.
type ListPage struct {
	Conversations []Conversation
	NextPageToken string
}

// ConversationsClient lists cloud chat conversations for the unified session list.
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

// ConversationsLaneActive mirrors Rust conversations_lane_active for Go builds:
// opt-in via GROK_SESSION_LIST_CONVERSATIONS or process-wide GROK_CHAT_MODE.
func ConversationsLaneActive() bool {
	return envTruthy("GROK_SESSION_LIST_CONVERSATIONS") || envTruthy("GROK_CHAT_MODE")
}

func envTruthy(key string) bool {
	value := strings.TrimSpace(strings.ToLower(os.Getenv(key)))
	switch value {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// ListConversations calls GET /rest/app-chat/conversations.
func (c *ConversationsClient) ListConversations(ctx context.Context, query ListQuery) (ListPage, error) {
	credential, err := auth.Load(c.AuthPath, c.AuthScope)
	if err != nil || credential.Key == "" || !credential.IsXAIAuth() {
		return ListPage{}, ErrNoOAuth
	}
	token := credential.Key
	if c.TokenProvider != nil {
		token, err = c.TokenProvider(ctx, "")
		if err != nil || token == "" {
			return ListPage{}, ErrNoOAuth
		}
	}
	base := strings.TrimRight(c.BaseURL, "/")
	if base == "" {
		base = ResolveConversationsBaseURL()
	}
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
	endpoint := base + "/rest/app-chat/conversations?" + values.Encode()

	request := func(accessToken string) (*http.Response, error) {
		req, requestErr := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
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
		client := c.HTTP
		if client == nil {
			client = http.DefaultClient
		}
		return client.Do(req)
	}

	response, err := request(token)
	if err != nil {
		return ListPage{}, err
	}
	if response.StatusCode == http.StatusUnauthorized && c.TokenProvider != nil {
		io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		response.Body.Close()
		token, err = c.TokenProvider(ctx, token)
		if err != nil || token == "" {
			return ListPage{}, ErrNoOAuth
		}
		response, err = request(token)
		if err != nil {
			return ListPage{}, err
		}
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return ListPage{}, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return ListPage{}, fmt.Errorf("conversations list failed: HTTP %d", response.StatusCode)
	}
	var wire struct {
		Conversations      []Conversation `json:"conversations"`
		NextPageToken      string         `json:"nextPageToken"`
		TextSearchMatches  []struct {
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
