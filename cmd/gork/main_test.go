package main

import (
	"bufio"
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
	"time"

	"github.com/lookcorner/go-cli/internal/api"
	"github.com/lookcorner/go-cli/internal/auth"
	"github.com/lookcorner/go-cli/internal/compat"
	"github.com/lookcorner/go-cli/internal/config"
	"github.com/lookcorner/go-cli/internal/mcp"
	"github.com/lookcorner/go-cli/internal/tools"
	"github.com/lookcorner/go-cli/internal/version"
	"github.com/lookcorner/go-cli/internal/workspace"
)

type samplingStreamer struct {
	request api.ResponseRequest
}

func TestSessionMCPRuntimeMergesAndRestoresConfiguration(t *testing.T) {
	disabled := false
	runtime := &sessionMCPRuntime{base: config.Config{MCPServers: map[string]config.MCPServerConfig{
		"base":     {Command: "base-server"},
		"disabled": {Command: "disabled-server", Enabled: &disabled},
	}}}
	_, effective := runtime.mergedConfig([]mcp.ServerConfig{
		{Name: "base", Command: "client-override"},
		{Name: "extra", Command: "extra-server"},
	})
	if len(effective) != 2 || effective[0].Name != "base" || effective[0].Command != "client-override" || effective[1].Name != "extra" {
		t.Fatalf("unexpected effective MCP configuration: %#v", effective)
	}

	root := t.TempDir()
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry(ws, nil)
	live := newSessionMCPRuntime(context.Background(), config.Config{}, root, registry, nil, nil, io.Discard)
	defer func() {
		live.Close()
		_ = registry.Close()
	}()
	if err := live.Update(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	err = live.Update(context.Background(), []mcp.ServerConfig{{Name: "broken", Command: filepath.Join(root, "missing-server")}})
	if err == nil {
		t.Fatal("invalid MCP update unexpectedly succeeded")
	}
	if configs := live.Configs(); len(configs) != 0 {
		t.Fatalf("failed update replaced previous MCP configuration: %#v", configs)
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodDelete {
			writer.WriteHeader(http.StatusNoContent)
			return
		}
		var rpc struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.NewDecoder(request.Body).Decode(&rpc); err != nil {
			t.Error(err)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		switch rpc.Method {
		case "initialize":
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"jsonrpc": "2.0", "id": rpc.ID, "result": map[string]any{
					"protocolVersion": "2025-11-25", "capabilities": map[string]any{"tools": map[string]any{}},
					"serverInfo": map[string]any{"name": "hot-base", "version": "1"},
				},
			})
		case "notifications/initialized":
			writer.WriteHeader(http.StatusAccepted)
		case "tools/list":
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"jsonrpc": "2.0", "id": rpc.ID, "result": map[string]any{"tools": []any{}},
			})
		default:
			t.Errorf("unexpected MCP method %q", rpc.Method)
			writer.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer server.Close()
	if err := live.UpdateBase(context.Background(), config.Config{MCPServers: map[string]config.MCPServerConfig{
		"hot-base": {Type: "http", URL: server.URL},
	}}); err != nil {
		t.Fatal(err)
	}
	if configs := live.Configs(); len(configs) != 1 || configs[0].Name != "hot-base" {
		t.Fatalf("hot base was not applied: %#v", configs)
	}
	if err := live.UpdateBase(context.Background(), config.Config{}); err != nil {
		t.Fatal(err)
	}
	if configs := live.Configs(); len(configs) != 0 {
		t.Fatalf("hot base was not removed: %#v", configs)
	}
	err = live.UpdateBase(context.Background(), config.Config{MCPServers: map[string]config.MCPServerConfig{
		"broken-base": {Command: filepath.Join(root, "missing-base-server")},
	}})
	if err == nil {
		t.Fatal("invalid MCP base update unexpectedly succeeded")
	}
	if len(live.base.MCPServers) != 0 || len(live.Configs()) != 0 {
		t.Fatalf("failed base update was not rolled back: base=%#v effective=%#v", live.base.MCPServers, live.Configs())
	}
}

func TestDiscoverSkillsLoadsConfiguredPlugin(t *testing.T) {
	root := t.TempDir()
	pluginRoot := filepath.Join(root, "plugin")
	skillDir := filepath.Join(pluginRoot, "skills", "deploy")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginRoot, "plugin.json"), []byte(`{"name":"team-tools","mcpServers":{"plugin-mcp":{"command":"${GROK_PLUGIN_ROOT}/server"}},"lspServers":{"plugin-lsp":{"command":"gopls","extensions":{".go":"go"}}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: deploy\ndescription: Deploy\n---\nDeploy"), 0o600); err != nil {
		t.Fatal(err)
	}
	pluginRoot, _ = filepath.EvalSymlinks(pluginRoot)
	workspaceCfg, catalog, _, err := discoverWorkspace(root, config.Config{Compat: compat.Default(), Plugins: config.PluginsConfig{Paths: []string{pluginRoot}}}, true)
	if err != nil {
		t.Fatal(err)
	}
	if names := strings.Join(catalog.Names(), "|"); names != "team-tools:deploy" {
		t.Fatalf("plugin skill names = %q", names)
	}
	if workspaceCfg.MCPServers["plugin-mcp"].Command != filepath.Join(pluginRoot, "server") {
		t.Fatalf("plugin MCP config = %#v", workspaceCfg.MCPServers)
	}
	if workspaceCfg.LSPServers["plugin-lsp"].Command != "gopls" || strings.Join(workspaceCfg.LSPServers["plugin-lsp"].Extensions, "|") != ".go" {
		t.Fatalf("plugin LSP config = %#v", workspaceCfg.LSPServers)
	}
}

func TestStartLSPServersRegistersDynamicToolWithoutInitialServers(t *testing.T) {
	root := t.TempDir()
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry(ws, nil)
	defer registry.Close()
	manager, err := startLSPServers(context.Background(), config.Config{}, ws, registry, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	if len(manager.Names()) != 0 {
		t.Fatalf("unexpected initial LSP servers: %#v", manager.Names())
	}
	found := false
	for _, tool := range registry.SnapshotTools() {
		if tool.Definition().Name == "lsp" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("dynamic LSP tool was not registered")
	}
}

func TestWatchMCPConfigReloadsChangedFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".mcp.json")
	if err := os.WriteFile(path, []byte(`{"mcpServers":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reloaded := make(chan struct{}, 1)
	watchMCPConfig(ctx, 5*time.Millisecond, func() ([]string, error) {
		return []string{path}, nil
	}, func() error {
		reloaded <- struct{}{}
		return nil
	}, io.Discard)
	if err := os.WriteFile(path, []byte(`{"mcpServers":{"added":{"command":"server"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-reloaded:
	case <-time.After(time.Second):
		t.Fatal("MCP config change was not reloaded")
	}
}

func TestRunPluginLifecycle(t *testing.T) {
	grokHome := filepath.Join(t.TempDir(), ".grok")
	t.Setenv("GROK_HOME", grokHome)
	source := filepath.Join(t.TempDir(), "source")
	if err := os.MkdirAll(filepath.Join(source, "skills", "cli"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "plugin.json"), []byte(`{"name":"cli-plugin","version":"1.0.0"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "skills", "cli", "SKILL.md"), []byte("---\nname: cli\ndescription: CLI\n---\nCLI"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if err := runPlugin([]string{"install", source}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "cli-plugin") {
		t.Fatalf("install output = %q", stdout.String())
	}
	cfg, err := config.Load("")
	if err != nil || strings.Join(cfg.Plugins.Enabled, "|") != "cli-plugin" {
		t.Fatalf("installed config=%#v err=%v", cfg.Plugins, err)
	}
	stdout.Reset()
	if err := runPlugin([]string{"list"}, &stdout, &stderr); err != nil || !strings.Contains(stdout.String(), "cli-plugin") {
		t.Fatalf("list output=%q err=%v", stdout.String(), err)
	}
	if err := os.WriteFile(filepath.Join(source, "plugin.json"), []byte(`{"name":"cli-plugin","version":"2.0.0"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	if err := runPlugin([]string{"update", "cli-plugin"}, &stdout, &stderr); err != nil || !strings.Contains(stdout.String(), "updated") {
		t.Fatalf("update output=%q err=%v", stdout.String(), err)
	}
	stdout.Reset()
	if err := runPlugin([]string{"uninstall", "cli-plugin"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	cfg, err = config.Load("")
	if err != nil || len(cfg.Plugins.Enabled) != 0 || !strings.Contains(stdout.String(), "Uninstalled") {
		t.Fatalf("uninstall output=%q config=%#v err=%v", stdout.String(), cfg.Plugins, err)
	}
}

func TestMCPHTTPHeadersUseBearerTokenEnvironment(t *testing.T) {
	t.Setenv("MCP_ACCESS_TOKEN", "secret")
	headers := mcpHTTPHeaders(config.MCPServerConfig{
		Headers:           map[string]string{"authorization": "Bearer old", "X-Test": "kept"},
		BearerTokenEnvVar: "MCP_ACCESS_TOKEN",
	})
	if headers["Authorization"] != "Bearer secret" || headers["X-Test"] != "kept" || len(headers) != 2 {
		t.Fatalf("headers = %#v", headers)
	}
}

func TestResolveProjectTrustPromptsAndPersists(t *testing.T) {
	previousVersion := version.Current
	version.Current = "1.0.0"
	t.Cleanup(func() { version.Current = previousVersion })
	t.Setenv("GROK_HOME", t.TempDir())
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".mcp.json"), []byte(`{"mcpServers":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{FolderTrustEnabled: true}
	var output bytes.Buffer
	trusted, err := resolveProjectTrust(context.Background(), root, cfg, false, bufio.NewReader(strings.NewReader("")), &output, false)
	if err != nil || trusted || !strings.Contains(output.String(), "--trust") {
		t.Fatalf("headless trust=%v output=%q err=%v", trusted, output.String(), err)
	}
	output.Reset()
	trusted, err = resolveProjectTrust(context.Background(), root, cfg, false, bufio.NewReader(strings.NewReader("yes\n")), &output, true)
	if err != nil || !trusted || !strings.Contains(output.String(), "Trust executable") {
		t.Fatalf("interactive trust=%v output=%q err=%v", trusted, output.String(), err)
	}
	trusted, err = resolveProjectTrust(context.Background(), root, cfg, false, bufio.NewReader(strings.NewReader("")), &output, false)
	if err != nil || !trusted {
		t.Fatalf("persisted trust=%v err=%v", trusted, err)
	}
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
	configPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(configPath, []byte("base_url = \""+server.URL+"\"\n[endpoints]\ncli_chat_proxy_base_url = \""+server.URL+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
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

func TestRunSetupInstallsManagedConfiguration(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	t.Setenv("GROK_DEPLOYMENT_KEY", "deployment-secret")
	managed := "[models]\ndefault = \"managed\"\n"
	requirements := "[auth]\npreferred_method = \"oidc\"\n"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer deployment-secret" {
			t.Fatalf("setup authorization=%q", request.Header.Get("Authorization"))
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"deployment_id": "deployment-1", "managed_config": managed, "requirements": requirements,
		})
	}))
	defer server.Close()
	path := filepath.Join(home, "config.toml")
	if err := os.WriteFile(path, []byte("[endpoints]\nmanaged_config_url = \""+server.URL+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	if err := run([]string{"setup", "--config", path}, strings.NewReader(""), &stdout, io.Discard); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "Managed configuration updated\n" {
		t.Fatalf("setup output=%q", stdout.String())
	}
	if data, err := os.ReadFile(filepath.Join(home, "managed_config.toml")); err != nil || string(data) != managed {
		t.Fatalf("managed config=%q err=%v", data, err)
	}
	if data, err := os.ReadFile(filepath.Join(home, "requirements.toml")); err != nil || string(data) != requirements {
		t.Fatalf("requirements=%q err=%v", data, err)
	}
}

func TestRunSetupRequiresManagedPrincipal(t *testing.T) {
	t.Setenv("GROK_HOME", t.TempDir())
	t.Setenv("GROK_DEPLOYMENT_KEY", "")
	err := run([]string{"setup"}, strings.NewReader(""), io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "GROK_DEPLOYMENT_KEY") {
		t.Fatalf("setup without principal error=%v", err)
	}
}

func TestRunSetupJSONDoesNotWritePolicy(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	t.Setenv("GROK_DEPLOYMENT_KEY", "deployment-secret")
	managed := "[auth]\npreferred_method = \"oidc\"\n"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"deployment_id": "deployment-1", "managed_config": managed,
		})
	}))
	defer server.Close()
	path := filepath.Join(home, "config.toml")
	if err := os.WriteFile(path, []byte("[endpoints]\nmanaged_config_url = \""+server.URL+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	if err := run([]string{"setup", "--json", "--config", path}, strings.NewReader(""), &stdout, io.Discard); err != nil {
		t.Fatal(err)
	}
	var report struct {
		Source       string `json:"source"`
		Configured   bool   `json:"configured"`
		DeploymentID string `json:"deploymentId"`
		Managed      string `json:"managedConfig"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Source != "deploymentKey" || !report.Configured || report.DeploymentID != "deployment-1" || report.Managed != managed {
		t.Fatalf("setup report=%#v", report)
	}
	if _, err := os.Stat(filepath.Join(home, "managed_config.toml")); !os.IsNotExist(err) {
		t.Fatalf("setup --json wrote policy: %v", err)
	}
}

func TestSessionStartRepairsAndReloadsMissingManagedPolicy(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	t.Setenv("GROK_DEPLOYMENT_KEY", "deployment-secret")
	t.Setenv("GORK_API_KEY", "")
	t.Setenv("XAI_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	requests := 0
	requirements := "[auth]\npreferred_method = \"api_key\"\n"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests++
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"deployment_id": "deployment-1", "requirements": requirements,
		})
	}))
	defer server.Close()
	path := filepath.Join(home, "config.toml")
	data := "[models]\ndefault = \"main\"\n[model.main]\nmodel = \"model\"\nbase_url = \"https://api.x.ai/v1\"\nbackend = \"responses\"\n[endpoints]\nmanaged_config_url = \"" + server.URL + "\"\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	err := run([]string{"--config", path, "hello"}, strings.NewReader(""), io.Discard, io.Discard)
	if requests != 1 || err == nil || !strings.Contains(err.Error(), "missing credentials") {
		t.Fatalf("session repair requests=%d err=%v", requests, err)
	}
	if data, err := os.ReadFile(filepath.Join(home, "requirements.toml")); err != nil || string(data) != requirements {
		t.Fatalf("repaired requirements=%q err=%v", data, err)
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

func TestRunLogoutClearsTeamManagedPolicy(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	t.Setenv("GROK_DEPLOYMENT_KEY", "")
	authConfig := auth.DefaultConfig()
	if err := auth.Save(filepath.Join(home, "auth.json"), authConfig.Scope(), auth.Credential{Key: "team-token", TeamID: "team-1"}); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{
		"managed_config.toml":      "[auth]\npreferred_method = \"oidc\"\n",
		"requirements.toml":        "[auth]\npreferred_method = \"oidc\"\n",
		"managed_config.sync.json": "{}\n",
	} {
		if err := os.WriteFile(filepath.Join(home, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := run([]string{"logout"}, strings.NewReader(""), io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"managed_config.toml", "requirements.toml", "managed_config.sync.json"} {
		if _, err := os.Stat(filepath.Join(home, name)); !os.IsNotExist(err) {
			t.Fatalf("team policy %s was not removed: %v", name, err)
		}
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

func TestRequirementsDenyCannotBeOverriddenByCLIAllow(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	data := []byte("[[permission.rules]]\naction = \"deny\"\ntool = \"bash\"\npattern = \"git push*\"\n")
	if err := os.WriteFile(filepath.Join(home, "requirements.toml"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(filepath.Join(home, "missing.toml"))
	if err != nil {
		t.Fatal(err)
	}
	allow, ask, deny, err := permissionRules(cfg.Permission, []string{"Bash(*)"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	approver, err := tools.NewPolicyApprover(
		tools.PromptApprover{Mode: tools.PermissionAuto}, tools.PromptApprover{Mode: tools.PermissionAuto},
		allow, ask, deny,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := approver.Approve(context.Background(), "shell", "git push origin main"); err == nil || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("CLI allow bypassed requirements deny: %v", err)
	}
}
