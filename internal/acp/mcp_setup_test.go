package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/config"
)

func TestMCPSetupPersistsPreferencesAndEnables(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(home, "config.toml")
	enabled := true
	if err := config.UpsertMCPServer(cfgPath, "acme", config.MCPServerConfig{
		URL:     "https://{{host}}/mcp",
		Enabled: &enabled,
		Setup: &config.MCPSetupConfig{
			Fields: []config.MCPSetupField{{
				ID: "site", Label: "Site", Type: "select",
				Options: []config.MCPSetupOption{{Label: "US", Value: "us"}},
			}},
			Variables: map[string]config.MCPSetupDerivedValue{
				"host": {From: "site", Map: map[string]string{"us": "us.example"}},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}

	reloaded := false
	enabledServer := ""
	var output bytes.Buffer
	server := &Server{
		output: &output,
		sessions: map[string]*session{
			"sess": {
				id:  "sess",
				cwd: root,
				runner: &agent.Runner{
					ReloadMCPBase: func(context.Context) error {
						reloaded = true
						return nil
					},
					ToggleMCPServer: func(_ context.Context, name string, on bool) error {
						if on {
							enabledServer = name
						}
						return nil
					},
					MCPServerCatalog: func() []MCPServer {
						return []MCPServer{{Name: "acme", URL: "https://us.example/mcp"}}
					},
				},
			},
		},
	}
	server.handleMCP(context.Background(), message{
		ID:     json.RawMessage("1"),
		Method: "x.ai/mcp/setup",
		Params: json.RawMessage(`{"sessionId":"sess","serverName":"acme","values":{"site":"us"}}`),
	})
	messages := decodeACPOutput(t, output.Bytes())
	if len(messages) != 1 {
		t.Fatalf("messages=%#v", messages)
	}
	result, _ := messages[0]["result"].(map[string]any)
	inner, _ := result["result"].(map[string]any)
	if inner["ok"] != true || result["error"] != nil {
		t.Fatalf("response=%#v", messages[0])
	}
	if !reloaded || enabledServer != "acme" {
		t.Fatalf("reloaded=%v enabled=%q", reloaded, enabledServer)
	}
	prefs := config.LoadMCPPreferences()
	if prefs.File.Servers["acme"].Values["site"] != "us" {
		t.Fatalf("prefs=%#v", prefs.File)
	}

	output.Reset()
	server.handleMCP(context.Background(), message{
		ID:     json.RawMessage("2"),
		Method: "x.ai/mcp/setup",
		Params: json.RawMessage(`{"sessionId":"sess","serverName":"missing","values":{}}`),
	})
	messages = decodeACPOutput(t, output.Bytes())
	if len(messages) != 1 {
		t.Fatalf("error messages=%#v", messages)
	}
	if messages[0]["error"] == nil && messages[0]["result"] == nil {
		t.Fatalf("expected setup error response=%#v", messages[0])
	}
}
