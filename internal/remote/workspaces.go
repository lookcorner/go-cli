package remote

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/lookcorner/go-cli/internal/api"
)

const defaultWorkspacesBaseURL = "https://grok.com"

// WorkspacesListQuery selects a page of grok.com workspaces.
type WorkspacesListQuery struct {
	PageSize  int
	PageToken string
	Query     string
	Kind      string
}

// WorkspacesListPage is one /rest/workspaces response.
type WorkspacesListPage struct {
	Workspaces    []Workspace
	NextPageToken string
}

// WorkspacesClient talks to grok.com /rest/workspaces.
type WorkspacesClient struct {
	HTTP          *http.Client
	BaseURL       string
	AuthPath      string
	AuthScope     string
	TokenProvider api.TokenProvider
}

// ResolveWorkspacesBaseURL mirrors the Rust client env precedence.
func ResolveWorkspacesBaseURL() string {
	for _, key := range []string{"GROK_WORKSPACES_BASE_URL", "GROK_CONVERSATIONS_BASE_URL", "GROK_CODE_WEB_URL"} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return strings.TrimRight(value, "/")
		}
	}
	return defaultWorkspacesBaseURL
}

// ListWorkspaces calls GET /rest/workspaces.
func (c *WorkspacesClient) ListWorkspaces(ctx context.Context, query WorkspacesListQuery) (WorkspacesListPage, error) {
	pageSize := query.PageSize
	if pageSize <= 0 {
		pageSize = 50
	}
	values := url.Values{}
	values.Set("pageSize", strconv.Itoa(pageSize))
	if token := strings.TrimSpace(query.PageToken); token != "" {
		values.Set("pageToken", token)
	}
	if search := strings.TrimSpace(query.Query); search != "" {
		values.Set("query", search)
	}
	if kind := strings.TrimSpace(query.Kind); kind != "" {
		values.Set("kind", kind)
	}

	bridge := &ConversationsClient{
		HTTP: c.HTTP, BaseURL: c.baseURL(), AuthPath: c.AuthPath, AuthScope: c.AuthScope, TokenProvider: c.TokenProvider,
	}
	data, err := bridge.do(ctx, http.MethodGet, "/rest/workspaces?"+values.Encode(), nil, false)
	if err != nil {
		return WorkspacesListPage{}, err
	}
	var wire struct {
		Workspaces    []Workspace `json:"workspaces"`
		NextPageToken string      `json:"nextPageToken"`
	}
	if json.Unmarshal(data, &wire) != nil {
		return WorkspacesListPage{}, errors.New("failed to parse workspaces response")
	}
	return WorkspacesListPage{
		Workspaces:    wire.Workspaces,
		NextPageToken: strings.TrimSpace(wire.NextPageToken),
	}, nil
}

func (c *WorkspacesClient) baseURL() string {
	if base := strings.TrimRight(strings.TrimSpace(c.BaseURL), "/"); base != "" {
		return base
	}
	return ResolveWorkspacesBaseURL()
}
