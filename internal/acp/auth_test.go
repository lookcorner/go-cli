package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/auth"
	"github.com/lookcorner/go-cli/internal/tools"
)

func TestAuthInfoAndBearerToken(t *testing.T) {
	path, scope := t.TempDir()+"/auth.json", "issuer::client"
	credential := auth.Credential{
		Key: "stored-token", AuthMode: "oidc", Email: "user@example.com", FirstName: "Ada", LastName: "Lovelace",
		ProfileImageAssetID: "asset-1", TeamID: "team-1", TeamName: "Core", TeamRole: "member",
		OrganizationID: "org-1", OrganizationName: "Example", OrganizationRole: "developer",
		PrincipalType: "Team", PrincipalID: "team-1", UserBlockedReason: "none",
		TeamBlockedReasons: []string{"BLOCKED_REASON_NO_LOGS"}, CodingDataRetentionOptOut: true,
	}
	if err := auth.Save(path, scope, credential); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	server := &Server{output: &output, Auth: AuthConfig{
		Path: path, Scope: scope, MethodID: "cached_token", Token: "stored-token",
		TokenProvider: func(context.Context, string) (string, error) { return "fresh-token", nil },
	}}
	server.handleAuth(context.Background(), message{ID: json.RawMessage("1"), Method: "x.ai/auth/info"})
	response := decodeACP(t, json.NewDecoder(&output))["result"].(map[string]any)
	if response["methodId"] != "cached_token" || response["email"] != credential.Email || response["firstName"] != credential.FirstName || response["profileImageUrl"] != "grok-asset:///asset-1" || response["teamId"] != credential.TeamID || response["organizationId"] != credential.OrganizationID || response["principalId"] != credential.PrincipalID || response["userBlockedReason"] != credential.UserBlockedReason || response["codingDataRetentionOptOut"] != true || response["teamBlockedReasons"].([]any)[0] != credential.TeamBlockedReasons[0] {
		t.Fatalf("auth info=%#v", response)
	}
	credential.ProfileImageAssetID = "https://assets.example/avatar.png"
	if err := auth.Save(path, scope, credential); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	server.handleAuth(context.Background(), message{ID: json.RawMessage("2"), Method: "x.ai/auth/info"})
	response = decodeACP(t, json.NewDecoder(&output))["result"].(map[string]any)
	if response["profileImageUrl"] != credential.ProfileImageAssetID {
		t.Fatalf("remote profile image=%#v", response)
	}

	output.Reset()
	server.handleAuth(context.Background(), message{ID: json.RawMessage("3"), Method: "x.ai/auth/getBearerToken"})
	response = decodeACP(t, json.NewDecoder(&output))["result"].(map[string]any)
	if response["token"] != "fresh-token" {
		t.Fatalf("bearer token=%#v", response)
	}
}

func TestAuthReadFallbacks(t *testing.T) {
	var output bytes.Buffer
	server := &Server{output: &output, Auth: AuthConfig{
		Token: "cached-token", TokenProvider: func(context.Context, string) (string, error) { return "", errors.New("offline") },
	}}
	server.handleAuth(context.Background(), message{ID: json.RawMessage("1"), Method: "x.ai/auth/getBearerToken"})
	response := decodeACP(t, json.NewDecoder(&output))["result"].(map[string]any)
	if response["token"] != "cached-token" {
		t.Fatalf("fallback token=%#v", response)
	}

	output.Reset()
	server.handleAuth(context.Background(), message{ID: json.RawMessage("2"), Method: "x.ai/auth/info"})
	response = decodeACP(t, json.NewDecoder(&output))["result"].(map[string]any)
	if response["methodId"] != nil || response["email"] != nil || response["profileImageUrl"] != nil || len(response["teamBlockedReasons"].([]any)) != 0 || response["codingDataRetentionOptOut"] != false {
		t.Fatalf("empty auth info=%#v", response)
	}
}

func TestAuthExtensionsRouteThroughServer(t *testing.T) {
	input := bytes.NewBufferString(
		`{"jsonrpc":"2.0","id":1,"method":"x.ai/auth/info","params":{}}` + "\n" +
			`{"jsonrpc":"2.0","id":2,"method":"x.ai/auth/getBearerToken","params":{}}` + "\n",
	)
	var output bytes.Buffer
	server := &Server{
		Auth: AuthConfig{MethodID: "xai.api_key", Token: "api-key"},
		Factory: func(context.Context, SessionConfig, tools.Approver, io.Writer, io.Writer) (*agent.Runner, func(), error) {
			return nil, nil, nil
		},
	}
	if err := server.Serve(context.Background(), input, &output); err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(&output)
	info := decodeACP(t, decoder)
	bearer := decodeACP(t, decoder)
	if info["id"] != float64(1) || info["result"].(map[string]any)["methodId"] != "xai.api_key" || bearer["id"] != float64(2) || bearer["result"].(map[string]any)["token"] != "api-key" {
		t.Fatalf("info=%#v bearer=%#v", info, bearer)
	}
}
