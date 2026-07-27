package mcpadmin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lookcorner/go-cli/internal/config"
	"github.com/lookcorner/go-cli/internal/mcp"
	"github.com/lookcorner/go-cli/internal/version"
)

func TestListMergesUserAndProjectConfigs(t *testing.T) {
	root := testRepo(t)
	nested := filepath.Join(root, "nested")
	if err := os.MkdirAll(filepath.Join(nested, ".grok"), 0o700); err != nil {
		t.Fatal(err)
	}
	userPath := filepath.Join(t.TempDir(), "config.toml")
	writeFile(t, userPath, "[mcp_servers.shared]\ncommand = \"user\"\n[mcp_servers.user]\ncommand = \"user-only\"\n")
	writeFile(t, filepath.Join(root, ".grok", "config.toml"), "[mcp_servers.shared]\ncommand = \"root\"\n[mcp_servers.root]\ncommand = \"root-only\"\n")
	writeFile(t, filepath.Join(nested, ".grok", "config.toml"), "[mcp_servers.shared]\ncommand = \"nested\"\n[disabled_mcp_servers]\n")

	entries, err := List(nested, userPath)
	if err != nil {
		t.Fatal(err)
	}
	byName := make(map[string]Entry, len(entries))
	for _, entry := range entries {
		byName[entry.Name] = entry
	}
	if len(entries) != 3 || byName["shared"].Config.Command != "nested" || byName["shared"].Scope != ProjectScope ||
		byName["user"].Scope != UserScope || byName["root"].Scope != ProjectScope {
		t.Fatalf("entries=%#v", entries)
	}
}

func TestAddAndRemoveLifecycle(t *testing.T) {
	root := testRepo(t)
	userPath := filepath.Join(t.TempDir(), "config.toml")
	writeFile(t, userPath, "[models]\ndefault = \"local\"\n")

	path, err := Add(root, userPath, AddRequest{
		Name: "local", Scope: UserScope, Transport: "stdio", Source: "npx",
		Args: []string{"-y", "server"}, Env: map[string]string{"TOKEN": "secret"},
	})
	if err != nil || path != userPath {
		t.Fatalf("path=%q err=%v", path, err)
	}
	cfg, err := config.Load(userPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MCPServers["local"].Command != "npx" || strings.Join(cfg.MCPServers["local"].Args, " ") != "-y server" ||
		cfg.MCPServers["local"].Env["TOKEN"] != "secret" || cfg.DefaultModelID != "local" {
		t.Fatalf("config=%#v", cfg)
	}
	scope, removedPath, err := Remove(root, userPath, "local", "")
	if err != nil || scope != UserScope || removedPath != userPath {
		t.Fatalf("scope=%q path=%q err=%v", scope, removedPath, err)
	}
	cfg, err = config.Load(userPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := cfg.MCPServers["local"]; exists || cfg.DefaultModelID != "local" {
		t.Fatalf("config after delete=%#v", cfg)
	}
}

func TestRemoveRequiresScopeWhenNameExistsTwice(t *testing.T) {
	root := testRepo(t)
	userPath := filepath.Join(t.TempDir(), "config.toml")
	writeFile(t, userPath, "[mcp_servers.shared]\ncommand = \"user\"\n")
	writeFile(t, filepath.Join(root, ".grok", "config.toml"), "[mcp_servers.shared]\ncommand = \"project\"\n")

	if _, _, err := Remove(root, userPath, "shared", ""); err == nil || !strings.Contains(err.Error(), "specify --scope") {
		t.Fatalf("ambiguous remove error=%v", err)
	}
	scope, path, err := Remove(root, userPath, "shared", ProjectScope)
	if err != nil || scope != ProjectScope || path != filepath.Join(root, ".grok", "config.toml") {
		t.Fatalf("scope=%q path=%q err=%v", scope, path, err)
	}
	remainingScope, remainingPath, found, err := RemainingDefinition(root, userPath, "shared")
	if err != nil || !found || remainingScope != UserScope || remainingPath != userPath {
		t.Fatalf("remaining scope=%q path=%q found=%v err=%v", remainingScope, remainingPath, found, err)
	}
}

func TestAddValidation(t *testing.T) {
	root := t.TempDir()
	tests := []AddRequest{
		{Name: "bad name", Source: "server"},
		{Name: "remote", Transport: "http", Source: "not-a-url"},
		{Name: "remote", Transport: "http", Source: "https://example.com", Env: map[string]string{"X": "1"}},
		{Name: "local", Transport: "stdio", Source: "server", Headers: map[string]string{"X": "1"}},
	}
	for _, request := range tests {
		if _, err := Add(root, filepath.Join(t.TempDir(), "config.toml"), request); err == nil {
			t.Fatalf("request accepted: %#v", request)
		}
	}
}

func TestDoctorReportsSuccessFailureAndDisabled(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("GROK_HOME", filepath.Join(home, ".grok"))
	root := testRepo(t)
	userPath := filepath.Join(t.TempDir(), "config.toml")
	writeFile(t, userPath, `
[mcp_servers.good]
command = "good"
[mcp_servers.bad]
url = "https://user:secret@example.com/mcp?token=secret"
type = "http"
[mcp_servers.off]
command = "off"
enabled = false
`)
	probed := make(map[string]bool)
	probe := func(_ context.Context, name string, _ config.MCPServerConfig, _ string) (ProbeResult, error) {
		probed[name] = true
		if name == "bad" {
			return ProbeResult{}, errors.New(`connect "https://user:secret@example.com/mcp?token=secret": denied`)
		}
		return ProbeResult{ProtocolVersion: "2025-11-25", ServerName: name, ToolCount: 2}, nil
	}
	report, err := Doctor(context.Background(), root, userPath, "", probe)
	if err != nil {
		t.Fatal(err)
	}
	if report.FailingCount != 2 || report.HealthyCount != 1 || len(report.Servers) != 3 || probed["off"] {
		t.Fatalf("report=%#v probed=%#v", report, probed)
	}
	for _, item := range report.Servers {
		data, _ := json.Marshal(item)
		if strings.Contains(string(data), "secret") || strings.Contains(string(data), "user:") {
			t.Fatalf("diagnostic leaked URL credentials: %s", data)
		}
	}
}

func TestDoctorReportsButDoesNotProbeUntrustedProjectServer(t *testing.T) {
	currentVersion := version.Current
	version.Current = "0.1.0"
	t.Cleanup(func() { version.Current = currentVersion })
	root := testRepo(t)
	userPath := filepath.Join(t.TempDir(), "config.toml")
	writeFile(t, filepath.Join(root, ".grok", "config.toml"), "[mcp_servers.project]\ncommand = \"project-server\"\n")
	probed := false
	report, err := Doctor(context.Background(), root, userPath, "", func(context.Context, string, config.MCPServerConfig, string) (ProbeResult, error) {
		probed = true
		return ProbeResult{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if probed || report.FailingCount != 1 || len(report.Servers) != 1 ||
		report.Servers[0].Checks[0].Label != "folder untrusted" {
		t.Fatalf("probed=%v report=%#v", probed, report)
	}
}

func TestDoctorOAuthCredentialsCheck(t *testing.T) {
	root := testRepo(t)
	grokHome := os.Getenv("GROK_HOME")
	userPath := filepath.Join(t.TempDir(), "config.toml")
	writeFile(t, userPath, `
[mcp_servers.needs]
url = "https://mcp.example/needs"
type = "http"
[mcp_servers.static]
url = "https://mcp.example/static"
type = "http"
headers = { Authorization = "Bearer static-token" }
[mcp_servers.env]
url = "https://mcp.example/env"
type = "http"
bearer_token_env_var = "DOCTOR_MCP_TOKEN"
[mcp_servers.stored]
url = "https://mcp.example/stored"
type = "http"
[mcp_servers.local]
command = "local-server"
`)
	t.Setenv("DOCTOR_MCP_TOKEN", "from-env")
	if err := os.MkdirAll(grokHome, 0o700); err != nil {
		t.Fatal(err)
	}
	storePath := filepath.Join(grokHome, "mcp_credentials.json")
	key := mcp.CredentialKey("stored", "https://mcp.example/stored")
	payload := `{"` + key + `":{"client_id":"c","token_response":{"access_token":"tok","token_type":"Bearer"}}}`
	if err := os.WriteFile(storePath, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}

	oldClient := oauthDiscoveryClient
	oauthDiscoveryClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("discovery disabled in credentials test")
	})}
	t.Cleanup(func() { oauthDiscoveryClient = oldClient })

	probed := map[string]bool{}
	report, err := Doctor(context.Background(), root, userPath, "", func(_ context.Context, name string, _ config.MCPServerConfig, _ string) (ProbeResult, error) {
		probed[name] = true
		return ProbeResult{ProtocolVersion: "2025-11-25", ToolCount: 1}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]DoctorEntry{}
	for _, item := range report.Servers {
		byName[item.Name] = item
	}
	needs := byName["needs"]
	if needs.Healthy || probed["needs"] || len(needs.Checks) < 2 || needs.Checks[0].Label != "oauth credentials" || needs.Checks[0].Passed {
		t.Fatalf("needs=%#v probed=%v", needs, probed["needs"])
	}
	if needs.Checks[1].Label != "oauth discovery" || needs.Checks[1].Passed {
		t.Fatalf("expected failed oauth discovery, got %#v", needs.Checks[1])
	}
	if !strings.Contains(needs.Checks[0].Hint, "auth_trigger") {
		t.Fatalf("hint=%q", needs.Checks[0].Hint)
	}
	for _, name := range []string{"static", "env", "stored", "local"} {
		item := byName[name]
		if !item.Healthy || !probed[name] {
			t.Fatalf("%s healthy=%v probed=%v checks=%#v", name, item.Healthy, probed[name], item.Checks)
		}
	}
	if byName["static"].Checks[0].Detail != "static bearer" || byName["env"].Checks[0].Detail != "bearer_token_env_var" ||
		byName["stored"].Checks[0].Detail != "stored credentials" {
		t.Fatalf("details static=%q env=%q stored=%q", byName["static"].Checks[0].Detail, byName["env"].Checks[0].Detail, byName["stored"].Checks[0].Detail)
	}
	if len(byName["local"].Checks) < 2 || byName["local"].Checks[0].Label == "oauth credentials" {
		t.Fatalf("stdio should skip oauth check: %#v", byName["local"].Checks)
	}
}

func TestOAuthDiscoveryCheckReportsTokenEndpoint(t *testing.T) {
	var serverURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/.well-known/oauth-protected-resource"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"authorization_servers": []string{serverURL},
			})
		case strings.Contains(r.URL.Path, "oauth-authorization-server") || strings.HasSuffix(r.URL.Path, "/.well-known/openid-configuration"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"issuer":                 serverURL,
				"authorization_endpoint": serverURL + "/authorize",
				"token_endpoint":         serverURL + "/token?secret=1",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	serverURL = server.URL

	oldClient := oauthDiscoveryClient
	oauthDiscoveryClient = server.Client()
	t.Cleanup(func() { oauthDiscoveryClient = oldClient })

	check, ok := oauthDiscoveryCheck(context.Background(), config.MCPServerConfig{URL: server.URL + "/mcp", Type: "http"})
	if !ok || !check.Passed {
		t.Fatalf("check=%#v ok=%v", check, ok)
	}
	if check.Label != "oauth discovery" || check.Detail != "token endpoint "+server.URL+"/token" {
		t.Fatalf("detail=%q", check.Detail)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func testRepo(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("GROK_HOME", filepath.Join(home, ".grok"))
	root := t.TempDir()
	if err := exec.Command("git", "init", "-q", root).Run(); err != nil {
		t.Skipf("git unavailable: %v", err)
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
