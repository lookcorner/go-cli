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

	"github.com/lookcorner/go-cli/internal/auth"
)

func TestListWorkspacesWireContract(t *testing.T) {
	authPath := filepath.Join(t.TempDir(), "auth.json")
	if err := auth.Save(authPath, "scope", auth.Credential{
		Key: "oauth-token", UserID: "user-1", Email: "a@x.ai",
		AuthMode: "oidc", Issuer: "https://auth.x.ai",
	}); err != nil {
		t.Fatal(err)
	}
	var seenQuery string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/workspaces" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer oauth-token" {
			t.Fatalf("auth=%q", got)
		}
		seenQuery = r.URL.RawQuery
		_ = json.NewEncoder(w).Encode(map[string]any{
			"workspaces": []map[string]any{{
				"workspaceId": "ws_9f3a",
				"name":        "GPU vendor research",
				"createTime":  "2026-06-18T17:30:00Z",
				"kind":        "WORKSPACE_KIND_IMAGINE",
			}},
			"nextPageToken": "tok2",
		})
	}))
	defer upstream.Close()

	client := &WorkspacesClient{
		HTTP: upstream.Client(), BaseURL: upstream.URL,
		AuthPath: authPath, AuthScope: "scope",
	}
	page, err := client.ListWorkspaces(context.Background(), WorkspacesListQuery{
		PageSize: 10, PageToken: "tok", Query: "gpu", Kind: "WORKSPACE_KIND_IMAGINE",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Workspaces) != 1 || page.Workspaces[0].WorkspaceID != "ws_9f3a" || page.Workspaces[0].Name != "GPU vendor research" {
		t.Fatalf("page=%#v", page)
	}
	if page.Workspaces[0].Kind != "WORKSPACE_KIND_IMAGINE" || page.NextPageToken != "tok2" {
		t.Fatalf("page=%#v", page)
	}
	if !strings.Contains(seenQuery, "pageSize=10") || !strings.Contains(seenQuery, "pageToken=tok") ||
		!strings.Contains(seenQuery, "query=gpu") || !strings.Contains(seenQuery, "kind=WORKSPACE_KIND_IMAGINE") {
		t.Fatalf("query=%q", seenQuery)
	}
}

func TestListWorkspacesRequiresXAIAuth(t *testing.T) {
	client := &WorkspacesClient{
		HTTP: http.DefaultClient, BaseURL: "http://127.0.0.1:1",
		AuthPath: filepath.Join(t.TempDir(), "missing.json"), AuthScope: "scope",
	}
	_, err := client.ListWorkspaces(context.Background(), WorkspacesListQuery{PageSize: 1})
	if !errors.Is(err, ErrNoOAuth) {
		t.Fatalf("err=%v", err)
	}
}
