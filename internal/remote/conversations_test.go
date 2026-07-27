package remote

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lookcorner/go-cli/internal/auth"
)

func TestListConversationsWireContract(t *testing.T) {
	authPath := filepath.Join(t.TempDir(), "auth.json")
	if err := auth.Save(authPath, "scope", auth.Credential{
		Key: "oauth-token", UserID: "user-1", Email: "a@x.ai",
		AuthMode: "oidc", Issuer: "https://auth.x.ai",
	}); err != nil {
		t.Fatal(err)
	}
	var seenQuery string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/app-chat/conversations" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer oauth-token" {
			t.Fatalf("auth=%q", got)
		}
		seenQuery = r.URL.RawQuery
		_ = json.NewEncoder(w).Encode(map[string]any{
			"conversations": []map[string]any{{
				"conversationId": "conv-1",
				"title":          "GPU vendors",
				"createTime":     "2026-06-18T17:30:00Z",
				"modifyTime":     "2026-06-18T18:02:00Z",
			}},
			"nextPageToken": "page-2",
		})
	}))
	defer upstream.Close()

	client := &ConversationsClient{
		HTTP: upstream.Client(), BaseURL: upstream.URL,
		AuthPath: authPath, AuthScope: "scope",
	}
	page, err := client.ListConversations(context.Background(), ListQuery{
		PageSize: 10, PageToken: "tok",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Conversations) != 1 || page.Conversations[0].ConversationID != "conv-1" || page.NextPageToken != "page-2" {
		t.Fatalf("page=%#v", page)
	}
	if !strings.Contains(seenQuery, "pageSize=10") || !strings.Contains(seenQuery, "pageToken=tok") {
		t.Fatalf("query=%q", seenQuery)
	}
}

func TestListConversationsSearchUsesTextMatches(t *testing.T) {
	authPath := filepath.Join(t.TempDir(), "auth.json")
	if err := auth.Save(authPath, "scope", auth.Credential{
		Key: "oauth-token", UserID: "user-1", AuthMode: "oidc", Issuer: "https://auth.x.ai",
	}); err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"conversations": []map[string]any{{
				"conversationId": "noise", "title": "Recent",
			}},
			"textSearchMatches": []map[string]any{{
				"conversation": map[string]any{
					"conversationId": "hit", "title": "Match",
					"modifyTime": "2026-06-18T18:02:00Z",
				},
			}},
		})
	}))
	defer upstream.Close()

	client := &ConversationsClient{HTTP: upstream.Client(), BaseURL: upstream.URL, AuthPath: authPath, AuthScope: "scope"}
	page, err := client.ListConversations(context.Background(), ListQuery{PageSize: 5, SearchQuery: "gpu"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Conversations) != 1 || page.Conversations[0].ConversationID != "hit" {
		t.Fatalf("page=%#v", page)
	}
}

func TestListConversationsRequiresXAIAuth(t *testing.T) {
	authPath := filepath.Join(t.TempDir(), "auth.json")
	if err := auth.Save(authPath, "scope", auth.Credential{Key: "api-key", AuthMode: "api_key"}); err != nil {
		t.Fatal(err)
	}
	client := &ConversationsClient{AuthPath: authPath, AuthScope: "scope"}
	_, err := client.ListConversations(context.Background(), ListQuery{PageSize: 1})
	if !errors.Is(err, ErrNoOAuth) {
		t.Fatalf("err=%v", err)
	}
}

func TestParseConversationTime(t *testing.T) {
	when := ParseConversationTime(Conversation{ModifyTime: "2026-06-18T18:02:00Z"})
	if !when.Equal(time.Date(2026, 6, 18, 18, 2, 0, 0, time.UTC)) {
		t.Fatalf("when=%v", when)
	}
}

func TestConversationsLaneActive(t *testing.T) {
	t.Setenv("GROK_SESSION_LIST_CONVERSATIONS", "")
	t.Setenv("GROK_CHAT_MODE", "")
	if ConversationsLaneActive() {
		t.Fatal("expected inactive")
	}
	t.Setenv("GROK_SESSION_LIST_CONVERSATIONS", "1")
	if !ConversationsLaneActive() {
		t.Fatal("expected session list gate")
	}
	t.Setenv("GROK_SESSION_LIST_CONVERSATIONS", "")
	t.Setenv("GROK_CHAT_MODE", "true")
	if !ConversationsLaneActive() {
		t.Fatal("expected chat mode gate")
	}
}
