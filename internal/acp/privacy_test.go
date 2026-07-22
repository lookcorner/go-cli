package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/auth"
	"github.com/lookcorner/go-cli/internal/tools"
	"github.com/lookcorner/go-cli/internal/version"
)

func TestSetCodingDataRetention(t *testing.T) {
	path, scope := filepath.Join(t.TempDir(), "auth.json"), "issuer::client"
	if err := auth.Save(path, scope, auth.Credential{Key: "token", AuthMode: "oidc"}); err != nil {
		t.Fatal(err)
	}
	requests := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		var body map[string]any
		if request.Method != http.MethodPut || json.NewDecoder(request.Body).Decode(&body) != nil || body["codingDataRetentionOptOut"] != true {
			t.Errorf("request method=%s body=%#v", request.Method, body)
		}
		if request.Header.Get("Authorization") != "Bearer token" || request.Header.Get("X-XAI-Token-Auth") != auth.DefaultTokenHeader || request.Header.Get("x-grok-client-version") != version.Current || request.Header.Get("x-grok-client-mode") != "interactive" {
			t.Errorf("request headers=%#v", request.Header)
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()

	var output bytes.Buffer
	server := &Server{output: &output, Auth: AuthConfig{Path: path, Scope: scope, ProxyBaseURL: upstream.URL}}
	server.handlePrivacy(context.Background(), message{ID: json.RawMessage("1"), Params: json.RawMessage(`{"codingDataRetentionOptOut":true}`)})
	result := decodeACP(t, json.NewDecoder(&output))["result"].(map[string]any)
	credential, err := auth.Load(path, scope)
	if result["codingDataRetentionOptOut"] != true || requests != 1 || err != nil || !credential.CodingDataRetentionOptOut {
		t.Fatalf("result=%#v requests=%d credential=%#v err=%v", result, requests, credential, err)
	}
}

func TestSetCodingDataRetentionRefreshesAfterUnauthorized(t *testing.T) {
	path, scope := filepath.Join(t.TempDir(), "auth.json"), "issuer::client"
	if err := auth.Save(path, scope, auth.Credential{Key: "old", RefreshToken: "refresh", AuthMode: "oidc"}); err != nil {
		t.Fatal(err)
	}
	var tokens []string
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		tokens = append(tokens, request.Header.Get("Authorization"))
		if len(tokens) == 1 {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		writer.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	provider := func(_ context.Context, rejected string) (string, error) {
		if rejected == "" {
			return "old", nil
		}
		if err := auth.Save(path, scope, auth.Credential{Key: "new", RefreshToken: "new-refresh", AuthMode: "oidc"}); err != nil {
			return "", err
		}
		return "new", nil
	}
	var output bytes.Buffer
	server := &Server{output: &output, Auth: AuthConfig{Path: path, Scope: scope, ProxyBaseURL: upstream.URL, HTTP: upstream.Client(), TokenProvider: provider}}
	server.handlePrivacy(context.Background(), message{ID: json.RawMessage("1"), Params: json.RawMessage(`{"codingDataRetentionOptOut":true}`)})
	result := decodeACP(t, json.NewDecoder(&output))["result"].(map[string]any)
	credential, err := auth.Load(path, scope)
	if result["codingDataRetentionOptOut"] != true || len(tokens) != 2 || tokens[0] != "Bearer old" || tokens[1] != "Bearer new" || err != nil || credential.Key != "new" || credential.RefreshToken != "new-refresh" || !credential.CodingDataRetentionOptOut {
		t.Fatalf("result=%#v tokens=%#v credential=%#v err=%v", result, tokens, credential, err)
	}
}

func TestSetCodingDataRetentionErrors(t *testing.T) {
	path, scope := filepath.Join(t.TempDir(), "auth.json"), "issuer::client"
	requests := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests++
		writer.WriteHeader(http.StatusInternalServerError)
		_, _ = writer.Write([]byte(`{"error":"privacy unavailable"}`))
	}))
	defer upstream.Close()
	var output bytes.Buffer
	server := &Server{output: &output, Auth: AuthConfig{Path: path, Scope: scope, ProxyBaseURL: upstream.URL, HTTP: upstream.Client()}}

	for _, params := range []string{`{}`, `{"codingDataRetentionOptOut":false}`, `{`} {
		output.Reset()
		server.handlePrivacy(context.Background(), message{ID: json.RawMessage("1"), Params: json.RawMessage(params)})
		errorValue := decodeACP(t, json.NewDecoder(&output))["error"].(map[string]any)
		if code := errorValue["code"]; code != float64(-32602) {
			t.Fatalf("params=%s code=%v", params, code)
		}
		if errorValue["message"] != "Invalid params" {
			t.Fatalf("params=%s response=%#v", params, errorValue)
		}
		if strings.Contains(params, "false") && errorValue["data"] != retentionLockedMessage {
			t.Fatalf("locked response=%#v", errorValue)
		}
	}
	if requests != 0 {
		t.Fatalf("invalid requests reached upstream: %d", requests)
	}

	output.Reset()
	server.handlePrivacy(context.Background(), message{ID: json.RawMessage("2"), Params: json.RawMessage(`{"codingDataRetentionOptOut":true}`)})
	errorValue := decodeACP(t, json.NewDecoder(&output))["error"].(map[string]any)
	if errorValue["code"] != float64(-32000) || errorValue["message"] != "Authentication required" || !strings.Contains(errorValue["data"].(string), "gork login") {
		t.Fatalf("missing auth=%#v", errorValue)
	}

	if err := auth.Save(path, scope, auth.Credential{Key: "token", AuthMode: "oidc"}); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	server.handlePrivacy(context.Background(), message{ID: json.RawMessage("3"), Params: json.RawMessage(`{"codingDataRetentionOptOut":true}`)})
	errorValue = decodeACP(t, json.NewDecoder(&output))["error"].(map[string]any)
	if errorValue["code"] != float64(-32603) || errorValue["message"] != "Internal error" || errorValue["data"] != "privacy unavailable" {
		t.Fatalf("upstream error=%#v", errorValue)
	}
}

type privacyRoundTripFunc func(*http.Request) (*http.Response, error)

func (f privacyRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestSetCodingDataRetentionProviderAndTransportErrors(t *testing.T) {
	path, scope := filepath.Join(t.TempDir(), "auth.json"), "issuer::client"
	if err := auth.Save(path, scope, auth.Credential{Key: "token", AuthMode: "oidc"}); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	server := &Server{output: &output, Auth: AuthConfig{
		Path: path, Scope: scope, ProxyBaseURL: "https://proxy.example",
		TokenProvider: func(context.Context, string) (string, error) { return "", errors.New("refresh failed") },
		HTTP: &http.Client{Transport: privacyRoundTripFunc(func(*http.Request) (*http.Response, error) {
			t.Fatal("provider failure must not reach HTTP")
			return nil, nil
		})},
	}}
	server.handlePrivacy(context.Background(), message{ID: json.RawMessage("1"), Params: json.RawMessage(`{"codingDataRetentionOptOut":true}`)})
	errorValue := decodeACP(t, json.NewDecoder(&output))["error"].(map[string]any)
	if errorValue["code"] != float64(-32000) || errorValue["message"] != "Authentication required" || !strings.Contains(errorValue["data"].(string), "gork login") {
		t.Fatalf("provider error=%#v", errorValue)
	}

	output.Reset()
	server.Auth.TokenProvider = nil
	server.Auth.HTTP = &http.Client{Transport: privacyRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("offline")
	})}
	server.handlePrivacy(context.Background(), message{ID: json.RawMessage("2"), Params: json.RawMessage(`{"codingDataRetentionOptOut":true}`)})
	errorValue = decodeACP(t, json.NewDecoder(&output))["error"].(map[string]any)
	if errorValue["code"] != float64(-32603) || errorValue["message"] != "Internal error" || !strings.Contains(errorValue["data"].(string), "offline") {
		t.Fatalf("transport error=%#v", errorValue)
	}
}

func TestPrivacyResponseError(t *testing.T) {
	for _, test := range []struct {
		body string
		want string
	}{
		{body: `{"error":"error detail"}`, want: "error detail"},
		{body: `{"message":"message detail"}`, want: "message detail"},
		{body: `not json`, want: "server returned HTTP 503"},
	} {
		response := &http.Response{StatusCode: http.StatusServiceUnavailable, Body: io.NopCloser(strings.NewReader(test.body))}
		if got := privacyResponseError(response); got != test.want {
			t.Fatalf("body=%q error=%q want=%q", test.body, got, test.want)
		}
	}
}

func TestPrivacyExtensionRoutesThroughServer(t *testing.T) {
	path, scope := filepath.Join(t.TempDir(), "auth.json"), "issuer::client"
	if err := auth.Save(path, scope, auth.Credential{Key: "token", AuthMode: "oidc"}); err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusOK) }))
	defer upstream.Close()
	input := bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"x.ai/privacy/setCodingDataRetention","params":{"codingDataRetentionOptOut":true}}` + "\n")
	var output bytes.Buffer
	server := &Server{
		Auth: AuthConfig{Path: path, Scope: scope, ProxyBaseURL: upstream.URL, HTTP: upstream.Client()},
		Factory: func(context.Context, SessionConfig, tools.Approver, io.Writer, io.Writer) (*agent.Runner, func(), error) {
			return nil, nil, nil
		},
	}
	if err := server.Serve(context.Background(), input, &output); err != nil {
		t.Fatal(err)
	}
	response := decodeACP(t, json.NewDecoder(&output))
	if response["id"] != float64(1) || response["result"].(map[string]any)["codingDataRetentionOptOut"] != true {
		t.Fatalf("response=%#v", response)
	}
}
