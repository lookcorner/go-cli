package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lookcorner/go-cli/internal/api"
	"github.com/lookcorner/go-cli/internal/auth"
	"github.com/lookcorner/go-cli/internal/config"
	"github.com/lookcorner/go-cli/internal/mcp"
	"github.com/lookcorner/go-cli/internal/tools"
)

type samplingStreamer struct {
	request api.ResponseRequest
}

func (s *samplingStreamer) StreamResponse(_ context.Context, request api.ResponseRequest, _ func(string)) (api.StreamResult, error) {
	s.request = request
	return api.StreamResult{Text: "sampled response"}, nil
}

func TestRunMCPSamplingMapsConversation(t *testing.T) {
	streamer := &samplingStreamer{}
	result, err := runMCPSampling(context.Background(), streamer, "sample-model", mcp.SamplingRequest{
		SystemPrompt: "Be concise", MaxTokens: 128,
		Messages: []mcp.SamplingMessage{
			{Role: "user", Content: mcp.SamplingContent{Type: "text", Text: "question"}},
			{Role: "assistant", Content: mcp.SamplingContent{Type: "text", Text: "prior answer"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Role != "assistant" || result.Content.Text != "sampled response" || result.Model != "sample-model" || result.StopReason != "endTurn" {
		t.Fatalf("unexpected sampling result: %#v", result)
	}
	request := streamer.request
	if request.Model != "sample-model" || request.Instructions != "Be concise" || request.MaxOutputTokens != 128 || len(request.Input) != 2 {
		t.Fatalf("unexpected model request: %#v", request)
	}
	if request.Input[0].Role != "user" || request.Input[0].Content != "question" || request.Input[1].Role != "assistant" {
		t.Fatalf("sampling messages were not preserved: %#v", request.Input)
	}
}

func TestRunMCPSamplingRejectsUnsupportedContent(t *testing.T) {
	_, err := runMCPSampling(context.Background(), &samplingStreamer{}, "model", mcp.SamplingRequest{
		Messages: []mcp.SamplingMessage{{Role: "user", Content: mcp.SamplingContent{Type: "audio"}}},
	})
	if err == nil {
		t.Fatal("unsupported sampling content was accepted")
	}
}

func TestMCPSamplingRequiresApproval(t *testing.T) {
	handler := newMCPSamplingHandler(config.Config{}, tools.PromptApprover{Mode: tools.PermissionDeny}, nil, "fixture")
	_, err := handler(context.Background(), mcp.SamplingRequest{
		MaxTokens: 1, Messages: []mcp.SamplingMessage{{Role: "user", Content: mcp.SamplingContent{Type: "text", Text: "private context"}}},
	})
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("unexpected approval error: %v", err)
	}
}

func TestLoginRejectsConflictingTransportsWithoutNetwork(t *testing.T) {
	err := run([]string{"login", "--oauth", "--device-auth"}, strings.NewReader(""), io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("unexpected login error: %v", err)
	}
}

func TestXAIBaseURLDetection(t *testing.T) {
	if !isXAIBaseURL("https://api.x.ai/v1") || isXAIBaseURL("https://api.x.ai.example/v1") || isXAIBaseURL("https://provider.example/v1") {
		t.Fatal("xAI base URL detection is incorrect")
	}
}

func TestBrowserCommandUsesPlatformLaunchersWithoutShell(t *testing.T) {
	rawURL := "https://accounts.x.ai/device?code=A&B"
	for _, test := range []struct {
		goos    string
		command string
		args    []string
	}{
		{"darwin", "open", []string{rawURL}},
		{"linux", "xdg-open", []string{rawURL}},
		{"windows", "rundll32", []string{"url.dll,FileProtocolHandler", rawURL}},
	} {
		command, args := browserCommand(test.goos, rawURL)
		if command != test.command || strings.Join(args, "\x00") != strings.Join(test.args, "\x00") {
			t.Fatalf("browser command for %s: %q %#v", test.goos, command, args)
		}
	}
	if command, args := browserCommand("linux", ""); command != "" || args != nil {
		t.Fatalf("empty URL should not produce a browser command: %q %#v", command, args)
	}
}

func TestRunLoginDeviceFlow(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/oauth2/device/code":
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"device_code": "device-1", "user_code": "ABCD-1234",
				"verification_uri": "http://127.0.0.1/verify", "expires_in": 600, "interval": 1,
			})
		case "/oauth2/token":
			_ = json.NewEncoder(writer).Encode(map[string]any{"access_token": "access-1", "refresh_token": "refresh-1", "expires_in": 3600})
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	authFile := filepath.Join(t.TempDir(), "auth.json")
	configPath := filepath.Join(t.TempDir(), "missing.toml")
	var stdout, stderr bytes.Buffer
	err := run([]string{
		"login", "--device-auth", "--issuer", server.URL, "--client-id", "client-1", "--scopes", "openid", "--auth-file", authFile, "--config", configPath, "--no-browser",
	}, strings.NewReader(""), &stdout, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "Signed in") || !strings.Contains(stderr.String(), "ABCD-1234") {
		t.Fatalf("unexpected login output: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	credential, err := auth.Load(authFile, (auth.Config{Issuer: server.URL, ClientID: "client-1"}).Scope())
	if err != nil || credential.Key != "access-1" || credential.RefreshToken != "refresh-1" {
		t.Fatalf("stored credential=%#v err=%v", credential, err)
	}
}

func TestRunLogoutRemovesSelectedScope(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	selected := auth.Config{Issuer: "https://auth.example", ClientID: "client-1"}
	if err := auth.Save(path, selected.Scope(), auth.Credential{Key: "remove"}); err != nil {
		t.Fatal(err)
	}
	if err := auth.Save(path, "sibling", auth.Credential{Key: "keep"}); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	if err := run([]string{"logout", "--issuer", selected.Issuer, "--client-id", selected.ClientID, "--auth-file", path}, strings.NewReader(""), &stdout, io.Discard); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "Signed out\n" {
		t.Fatalf("logout output=%q", stdout.String())
	}
	if _, err := auth.Load(path, selected.Scope()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("selected scope still loads: %v", err)
	}
	if credential, err := auth.Load(path, "sibling"); err != nil || credential.Key != "keep" {
		t.Fatalf("sibling credential=%#v err=%v", credential, err)
	}
}

func TestTeamPolicyDisablesStaticAPIKey(t *testing.T) {
	t.Setenv("GROK_HOME", t.TempDir())
	t.Setenv("GORK_API_KEY", "must-not-bypass-team-policy")
	t.Setenv("XAI_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	path := filepath.Join(t.TempDir(), "config.toml")
	data := []byte(`
[models]
default = "main"

[model.main]
model = "model"
base_url = "https://api.x.ai/v1"
backend = "responses"

[grok_com_config]
force_login_team_uuid = "team-required"
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	err := run([]string{"--config", path, "hello"}, strings.NewReader(""), io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "missing credentials") {
		t.Fatalf("static API key bypassed team policy: %v", err)
	}
}

func TestPreferredAuthMethodFailsClosed(t *testing.T) {
	for _, method := range []string{"oidc", "api_key"} {
		t.Run(method, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("GROK_HOME", home)
			t.Setenv("GROK_OIDC_ISSUER", "")
			t.Setenv("GROK_OIDC_CLIENT_ID", "")
			t.Setenv("GROK_OAUTH2_ISSUER", "")
			t.Setenv("GROK_OAUTH2_CLIENT_ID", "")
			t.Setenv("XAI_API_KEY", "")
			t.Setenv("OPENAI_API_KEY", "")
			if method == "oidc" {
				t.Setenv("GORK_API_KEY", "static-must-be-ignored")
			} else {
				t.Setenv("GORK_API_KEY", "")
				path, err := auth.DefaultPath()
				if err != nil {
					t.Fatal(err)
				}
				cfg := auth.DefaultConfig()
				if err := auth.Save(path, cfg.Scope(), auth.Credential{Key: "session-must-be-ignored"}); err != nil {
					t.Fatal(err)
				}
			}
			path := filepath.Join(t.TempDir(), "config.toml")
			data := []byte("[models]\ndefault = \"main\"\n[model.main]\nmodel = \"model\"\nbase_url = \"https://api.x.ai/v1\"\nbackend = \"responses\"\n[auth]\npreferred_method = \"" + method + "\"\n")
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			err := run([]string{"--config", path, "hello"}, strings.NewReader(""), io.Discard, io.Discard)
			if err == nil || !strings.Contains(err.Error(), "missing credentials") {
				t.Fatalf("preferred method %s fell back: %v", method, err)
			}
		})
	}
}
