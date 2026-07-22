package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
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

func TestCheckSubscriptionReturnsApplicationAuthMeta(t *testing.T) {
	var output bytes.Buffer
	called := 0
	gateURL, gateLabel := "https://example.com/subscribe", "Subscribe"
	showResolvedModel := false
	server := &Server{output: &output, Auth: AuthConfig{CheckSubscription: func(context.Context) SubscriptionCheckResult {
		called++
		email, mode, tier := "user@example.com", "Oidc", "SuperGrok"
		return SubscriptionCheckResult{Authenticated: true, Meta: &AuthMeta{
			Email: &email, AuthMode: &mode, IsZDR: true, CodingDataRetentionOptOut: true,
			ShowResolvedModel: &showResolvedModel,
			Gate:              &AuthGate{Message: "Upgrade", URL: &gateURL, Label: &gateLabel}, SubscriptionTier: &tier,
		}}
	}}}
	server.handleAuth(context.Background(), message{ID: json.RawMessage("1"), Method: "x.ai/auth/check_subscription", Params: json.RawMessage(`{}`)})
	result := decodeACP(t, json.NewDecoder(&output))["result"].(map[string]any)
	meta := result["meta"].(map[string]any)
	gate := meta["gate"].(map[string]any)
	if called != 1 || result["authenticated"] != true || meta["email"] != "user@example.com" || meta["auth_mode"] != "Oidc" || meta["is_zdr"] != true || meta["coding_data_retention_opt_out"] != true || meta["show_resolved_model"] != false || meta["subscription_tier"] != "SuperGrok" || gate["message"] != "Upgrade" || gate["url"] != gateURL || gate["label"] != gateLabel {
		t.Fatalf("called=%d result=%#v", called, result)
	}

	output.Reset()
	server.Auth.CheckSubscription = nil
	server.handleAuth(context.Background(), message{ID: json.RawMessage("2"), Method: "x.ai/auth/check_subscription", Params: json.RawMessage(`{}`)})
	result = decodeACP(t, json.NewDecoder(&output))["result"].(map[string]any)
	if result["authenticated"] != false || result["meta"] != nil {
		t.Fatalf("unsupported result=%#v", result)
	}
}

func TestAuthLogoutClearsCurrentCredentialAndCachedState(t *testing.T) {
	withoutAuthEnvironment(t)
	path, scope := filepath.Join(t.TempDir(), "auth.json"), "issuer::client"
	if err := auth.Save(path, scope, auth.Credential{Key: "stored-token", Email: "user@example.com"}); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	callbacks := 0
	server := &Server{output: &output, Auth: AuthConfig{
		Path: path, Scope: scope, MethodID: "cached_token", Token: "stored-token",
		TokenProvider: func(context.Context, string) (string, error) { return "", os.ErrNotExist },
	}, AuthChanged: func(_ context.Context, result auth.LogoutResult) error {
		callbacks++
		if !result.ClearedCurrent || result.Email != "user@example.com" {
			t.Fatalf("callback result=%#v", result)
		}
		return nil
	}}

	server.handleAuth(context.Background(), message{ID: json.RawMessage("1"), Method: "x.ai/auth/logout", Params: json.RawMessage(`{}`)})
	response := decodeACP(t, json.NewDecoder(&output))["result"].(map[string]any)
	if response["ok"] != true || response["was_logged_in"] != true || response["email"] != "user@example.com" || response["api_key_still_set"] != false || callbacks != 1 {
		t.Fatalf("logout response=%#v callbacks=%d", response, callbacks)
	}
	if server.Auth.MethodID != "" || server.Auth.Token != "" {
		t.Fatalf("cached auth state=%#v", server.Auth)
	}
	if _, err := auth.Load(path, scope); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("current credential still loads: %v", err)
	}

	output.Reset()
	server.AuthChanged = nil
	server.handleAuth(context.Background(), message{ID: json.RawMessage("2"), Method: "x.ai/auth/logout", Params: json.RawMessage(`{}`)})
	response = decodeACP(t, json.NewDecoder(&output))["result"].(map[string]any)
	if response["ok"] != true || response["was_logged_in"] != false || response["email"] != nil || response["api_key_still_set"] != false {
		t.Fatalf("second logout response=%#v", response)
	}

	output.Reset()
	server.handleAuth(context.Background(), message{ID: json.RawMessage("3"), Method: "x.ai/auth/getBearerToken"})
	response = decodeACP(t, json.NewDecoder(&output))["result"].(map[string]any)
	if response["token"] != nil {
		t.Fatalf("stale bearer token=%#v", response)
	}
	output.Reset()
	server.handleAuth(context.Background(), message{ID: json.RawMessage("4"), Method: "x.ai/auth/info"})
	response = decodeACP(t, json.NewDecoder(&output))["result"].(map[string]any)
	if response["methodId"] != nil || response["email"] != nil {
		t.Fatalf("stale auth info=%#v", response)
	}
}

func TestAuthLogoutExplicitScopePreservesCurrentState(t *testing.T) {
	path, scope := filepath.Join(t.TempDir(), "auth.json"), "issuer::client"
	if err := auth.Save(path, scope, auth.Credential{Key: "current-token"}); err != nil {
		t.Fatal(err)
	}
	if err := auth.Save(path, "sibling", auth.Credential{Key: "sibling-token"}); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	server := &Server{output: &output, Auth: AuthConfig{Path: path, Scope: scope, MethodID: "cached_token", Token: "current-token"}}
	server.handleAuth(context.Background(), message{ID: json.RawMessage("1"), Method: "x.ai/auth/logout", Params: json.RawMessage(`{"scope":"sibling"}`)})
	response := decodeACP(t, json.NewDecoder(&output))["result"].(map[string]any)
	if response["was_logged_in"] != true || server.Auth.MethodID != "cached_token" || server.Auth.Token != "current-token" {
		t.Fatalf("response=%#v auth=%#v", response, server.Auth)
	}
	if _, err := auth.Load(path, "sibling"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("sibling credential still loads: %v", err)
	}
	if current, err := auth.Load(path, scope); err != nil || current.Key != "current-token" {
		t.Fatalf("current=%#v err=%v", current, err)
	}
}

func TestAuthLogoutErrors(t *testing.T) {
	var output bytes.Buffer
	server := &Server{output: &output, Auth: AuthConfig{Path: filepath.Join(t.TempDir(), "auth.json"), Scope: "scope"}}
	server.handleAuth(context.Background(), message{ID: json.RawMessage("1"), Method: "x.ai/auth/logout", Params: json.RawMessage(`{`)})
	response := decodeACP(t, json.NewDecoder(&output))["error"].(map[string]any)
	if response["code"] != float64(-32602) || response["message"] != "Invalid params" || response["data"] != "invalid params" {
		t.Fatalf("invalid params response=%#v", response)
	}
	for _, params := range []string{"", ` null `, `[]`, `{"scope":42}`} {
		output.Reset()
		server.handleAuth(context.Background(), message{ID: json.RawMessage("1"), Method: "x.ai/auth/logout", Params: json.RawMessage(params)})
		response = decodeACP(t, json.NewDecoder(&output))["error"].(map[string]any)
		if response["code"] != float64(-32602) || response["message"] != "Invalid params" || response["data"] != "invalid params" {
			t.Fatalf("params=%q response=%#v", params, response)
		}
	}

	corrupt := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(corrupt, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	server.Auth.Path = corrupt
	server.handleAuth(context.Background(), message{ID: json.RawMessage("2"), Method: "x.ai/auth/logout", Params: json.RawMessage(`{}`)})
	response = decodeACP(t, json.NewDecoder(&output))["error"].(map[string]any)
	if response["code"] != float64(-32603) || response["message"] != "Internal error" || !strings.HasPrefix(response["data"].(string), "failed to logout: decode auth store:") {
		t.Fatalf("logout error response=%#v", response)
	}

	path := filepath.Join(t.TempDir(), "auth.json")
	if err := auth.Save(path, "scope", auth.Credential{Key: "token"}); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	server.Auth.Path = path
	server.AuthChanged = func(context.Context, auth.LogoutResult) error { return errors.New("reload failed") }
	server.handleAuth(context.Background(), message{ID: json.RawMessage("3"), Method: "x.ai/auth/logout", Params: json.RawMessage(`{}`)})
	response = decodeACP(t, json.NewDecoder(&output))["error"].(map[string]any)
	if response["code"] != float64(-32603) || response["message"] != "Internal error" || response["data"] != "failed to refresh authentication state: reload failed" {
		t.Fatalf("callback error response=%#v", response)
	}
}

func TestAuthClearedRefreshesRuntimeWithoutClearingAPIKey(t *testing.T) {
	var output bytes.Buffer
	called := 0
	server := &Server{output: &output, Auth: AuthConfig{MethodID: "xai.api_key", Token: "api-key"}, AuthChanged: func(_ context.Context, result auth.LogoutResult) error {
		called++
		if !result.ClearedCurrent {
			t.Fatalf("callback result=%#v", result)
		}
		return nil
	}}
	server.handleAuth(context.Background(), message{ID: json.RawMessage("1"), Method: "x.ai/internal/auth_cleared", Params: json.RawMessage(`{}`)})
	response := decodeACP(t, json.NewDecoder(&output))["result"].(map[string]any)
	if response["ok"] != true || called != 1 || server.Auth.MethodID != "xai.api_key" || server.Auth.Token != "api-key" {
		t.Fatalf("response=%#v called=%d auth=%#v", response, called, server.Auth)
	}
}

func TestAuthLogoutPreservesStaticAPIKeyState(t *testing.T) {
	path, scope := filepath.Join(t.TempDir(), "auth.json"), "issuer::client"
	if err := auth.Save(path, scope, auth.Credential{Key: "oauth-token"}); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	server := &Server{output: &output, Auth: AuthConfig{Path: path, Scope: scope, MethodID: "xai.api_key", Token: "api-key"}}
	server.handleAuth(context.Background(), message{ID: json.RawMessage("1"), Method: "x.ai/auth/logout", Params: json.RawMessage(`{}`)})
	response := decodeACP(t, json.NewDecoder(&output))["result"].(map[string]any)
	if response["ok"] != true || server.Auth.MethodID != "xai.api_key" || server.Auth.Token != "api-key" {
		t.Fatalf("response=%#v auth=%#v", response, server.Auth)
	}
}

func withoutAuthEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range []string{"XAI_API_KEY", "GROK_CODE_XAI_API_KEY"} {
		value, present := os.LookupEnv(name)
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if present {
				_ = os.Setenv(name, value)
			} else {
				_ = os.Unsetenv(name)
			}
		})
	}
}

func TestAPIKeyExtensions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	t.Setenv("XAI_API_KEY", "environment-key")
	t.Setenv("GROK_CODE_XAI_API_KEY", "legacy-key")
	var output bytes.Buffer
	server := &Server{output: &output, Auth: AuthConfig{Path: path}}

	server.handleAuth(context.Background(), message{ID: json.RawMessage("1"), Method: "x.ai/getApiKey"})
	response := decodeACP(t, json.NewDecoder(&output))["result"].(map[string]any)
	if response["key"] != "environment-key" {
		t.Fatalf("API key response=%#v", response)
	}

	output.Reset()
	server.handleAuth(context.Background(), message{ID: json.RawMessage("2"), Method: "x.ai/setApiKey", Params: json.RawMessage(`{"key":"stored-key"}`)})
	response = decodeACP(t, json.NewDecoder(&output))["result"].(map[string]any)
	credential, err := auth.Load(path, auth.APIKeyScope)
	if response["ok"] != true || err != nil || credential.Key != "stored-key" || os.Getenv("XAI_API_KEY") != "stored-key" {
		t.Fatalf("set response=%#v credential=%#v env=%q err=%v", response, credential, os.Getenv("XAI_API_KEY"), err)
	}

	for _, params := range []string{`{"key":""}`, `{}`, `{"key":null}`, `{"key":42}`, `[]`, ``} {
		if err := auth.StoreAPIKey(path, "stored-key"); err != nil {
			t.Fatal(err)
		}
		if err := os.Setenv("XAI_API_KEY", "stored-key"); err != nil {
			t.Fatal(err)
		}
		output.Reset()
		server.handleAuth(context.Background(), message{ID: json.RawMessage("3"), Method: "x.ai/setApiKey", Params: json.RawMessage(params)})
		response = decodeACP(t, json.NewDecoder(&output))["result"].(map[string]any)
		_, loadErr := auth.Load(path, auth.APIKeyScope)
		if response["ok"] != true || !errors.Is(loadErr, os.ErrNotExist) {
			t.Fatalf("clear params=%s response=%#v loadErr=%v", params, response, loadErr)
		}
		if _, present := os.LookupEnv("XAI_API_KEY"); present {
			t.Fatalf("clear params=%q left XAI_API_KEY set", params)
		}
		if os.Getenv("GROK_CODE_XAI_API_KEY") != "legacy-key" {
			t.Fatalf("clear params=%q changed the legacy API key", params)
		}
	}

	if err := os.Unsetenv("GROK_CODE_XAI_API_KEY"); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	server.handleAuth(context.Background(), message{ID: json.RawMessage("4"), Method: "x.ai/getApiKey"})
	response = decodeACP(t, json.NewDecoder(&output))["result"].(map[string]any)
	if response["key"] != nil {
		t.Fatalf("missing API key response=%#v", response)
	}
}

func TestSetAPIKeyErrors(t *testing.T) {
	var output bytes.Buffer
	server := &Server{output: &output, Auth: AuthConfig{Path: filepath.Join(t.TempDir(), "auth.json")}}
	server.handleAuth(context.Background(), message{ID: json.RawMessage("1"), Method: "x.ai/setApiKey", Params: json.RawMessage(`{`)})
	response := decodeACP(t, json.NewDecoder(&output))["error"].(map[string]any)
	if response["code"] != float64(-32602) {
		t.Fatalf("malformed params response=%#v", response)
	}

	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	server.Auth.Path = filepath.Join(blocker, "auth.json")
	server.handleAuth(context.Background(), message{ID: json.RawMessage("2"), Method: "x.ai/setApiKey", Params: json.RawMessage(`{"key":"secret"}`)})
	response = decodeACP(t, json.NewDecoder(&output))["error"].(map[string]any)
	if response["code"] != float64(-32000) {
		t.Fatalf("persistence error response=%#v", response)
	}
}

func TestAuthExtensionsRouteThroughServer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	t.Setenv("XAI_API_KEY", "environment-key")
	input := bytes.NewBufferString(
		`{"jsonrpc":"2.0","id":1,"method":"x.ai/auth/info","params":{}}` + "\n" +
			`{"jsonrpc":"2.0","id":2,"method":"x.ai/auth/getBearerToken","params":{}}` + "\n" +
			`{"jsonrpc":"2.0","id":3,"method":"x.ai/getApiKey","params":{}}` + "\n" +
			`{"jsonrpc":"2.0","id":4,"method":"x.ai/setApiKey","params":{"key":"updated-key"}}` + "\n" +
			`{"jsonrpc":"2.0","id":5,"method":"x.ai/auth/check_subscription","params":{}}` + "\n" +
			`{"jsonrpc":"2.0","id":6,"method":"x.ai/auth/logout","params":{}}` + "\n" +
			`{"jsonrpc":"2.0","id":7,"method":"x.ai/internal/auth_cleared","params":{}}` + "\n",
	)
	var output bytes.Buffer
	server := &Server{
		Auth: AuthConfig{Path: path, MethodID: "xai.api_key", Token: "api-key", CheckSubscription: func(context.Context) SubscriptionCheckResult {
			return SubscriptionCheckResult{Authenticated: true, Meta: &AuthMeta{}}
		}},
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
	getAPIKey := decodeACP(t, decoder)
	setAPIKey := decodeACP(t, decoder)
	checkSubscription := decodeACP(t, decoder)
	logout := decodeACP(t, decoder)
	authCleared := decodeACP(t, decoder)
	if info["id"] != float64(1) || info["result"].(map[string]any)["methodId"] != "xai.api_key" || bearer["id"] != float64(2) || bearer["result"].(map[string]any)["token"] != "api-key" || getAPIKey["result"].(map[string]any)["key"] != "environment-key" || setAPIKey["result"].(map[string]any)["ok"] != true || checkSubscription["result"].(map[string]any)["authenticated"] != true || logout["result"].(map[string]any)["ok"] != true || authCleared["result"].(map[string]any)["ok"] != true {
		t.Fatalf("info=%#v bearer=%#v get=%#v set=%#v check=%#v logout=%#v cleared=%#v", info, bearer, getAPIKey, setAPIKey, checkSubscription, logout, authCleared)
	}
}
