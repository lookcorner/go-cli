package remote

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/lookcorner/go-cli/internal/auth"
)

func TestListModesWireContract(t *testing.T) {
	authPath := filepath.Join(t.TempDir(), "auth.json")
	if err := auth.Save(authPath, "scope", auth.Credential{
		Key: "api-or-oauth", UserID: "user-1", AuthMode: "api_key",
	}); err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/rest/modes" {
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"modes": []map[string]any{{
				"id": "auto", "title": "Auto", "description": "Picks best",
				"badgeText": "New", "availability": map[string]any{"available": map[string]any{}},
				"iconHint": "rocket", "tags": []string{"TAG_PRIMARY"},
			}, {
				"id": "heavy", "title": "Heavy",
				"availability": map[string]any{"requiresUpgrade": map[string]any{"message": "Upgrade"}},
			}},
			"defaultModeId": "auto",
		})
	}))
	defer upstream.Close()

	client := &ChatModesClient{HTTP: upstream.Client(), BaseURL: upstream.URL, AuthPath: authPath, AuthScope: "scope"}
	page, err := client.ListModes(context.Background(), "en")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Modes) != 2 || page.DefaultModeID != "auto" || !page.Modes[0].IsAvailable() || page.Modes[1].IsAvailable() {
		t.Fatalf("page=%#v", page)
	}
	state := page.ToModelState()
	if state.CurrentModelID != "auto" || len(state.Available) != 1 || state.Available[0].Meta["badgeText"] != "New" {
		t.Fatalf("state=%#v", state)
	}
}

func TestListModesRequiresAuth(t *testing.T) {
	client := &ChatModesClient{AuthPath: filepath.Join(t.TempDir(), "missing.json"), AuthScope: "scope"}
	_, err := client.ListModes(context.Background(), "en")
	if !errors.Is(err, ErrNoAuth) {
		t.Fatalf("err=%v", err)
	}
}
