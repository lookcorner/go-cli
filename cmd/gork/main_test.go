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

func TestLoginRejectsDisabledDeviceAuthWithoutNetwork(t *testing.T) {
	err := run([]string{"login", "--device-auth=false"}, strings.NewReader(""), io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "only device authentication") {
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
	var stdout, stderr bytes.Buffer
	err := run([]string{
		"login", "--issuer", server.URL, "--client-id", "client-1", "--scopes", "openid", "--auth-file", authFile, "--no-browser",
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
