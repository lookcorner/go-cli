package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lookcorner/go-cli/internal/version"
)

func TestEnrichMergesRemoteProfile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/user" || request.Header.Get("Authorization") != "Bearer access-1" {
			t.Fatalf("unexpected request: %s %#v", request.URL, request.Header)
		}
		if request.Header.Get("X-XAI-Token-Auth") != "custom-client" || request.Header.Get("x-grok-client-version") != version.Current {
			t.Fatalf("missing profile headers: %#v", request.Header)
		}
		_, _ = writer.Write([]byte(`{
			"userId":"remote-user","email":"remote@example.com","firstName":"Ada",
			"teamId":"team-2","teamName":"Core","organizationId":"org-1",
			"teamBlockedReasons":[],"codingDataRetentionOptOut":false
		}`))
	}))
	defer server.Close()

	original := Credential{
		Key: "access-1", RefreshToken: "refresh-1", UserID: "token-user", LastName: "Existing",
		TeamBlockedReasons: []string{"old"},
	}
	got := NewClient(server.Client()).Enrich(context.Background(), server.URL+"/v1/", "custom-client", original)
	if got.UserID != "remote-user" || got.Email != "remote@example.com" || got.FirstName != "Ada" || got.LastName != "Existing" {
		t.Fatalf("unexpected enriched profile: %#v", got)
	}
	if got.TeamID != "team-2" || got.TeamName != "Core" || got.OrganizationID != "org-1" || len(got.TeamBlockedReasons) != 0 {
		t.Fatalf("team profile was not merged: %#v", got)
	}
	if got.Key != original.Key || got.RefreshToken != original.RefreshToken || !got.CodingDataRetentionOptOut {
		t.Fatalf("token fields or privacy lock changed incorrectly: %#v", got)
	}
}

func TestEnrichIsBestEffort(t *testing.T) {
	for _, body := range []string{`{"userId":""}`, `{broken`} {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			_, _ = writer.Write([]byte(body))
		}))
		original := Credential{Key: "access-1", UserID: "existing", Email: "existing@example.com"}
		got := NewClient(server.Client()).Enrich(context.Background(), server.URL, "", original)
		server.Close()
		if got.UserID != original.UserID || got.Email != original.Email {
			t.Fatalf("failed enrichment changed credential: %#v", got)
		}
	}
}
