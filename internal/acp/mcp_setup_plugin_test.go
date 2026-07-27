package acp

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/plugin"
)

func TestMCPListIncludesPluginSetupPlaceholders(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	pluginRoot := t.TempDir()
	mcpPath := filepath.Join(pluginRoot, ".mcp.json")
	raw, err := json.Marshal(map[string]any{
		"mcpServers": map[string]any{
			"plugin-acme": map[string]any{
				"url": "https://{{host}}/mcp",
				"setup": map[string]any{
					"fields": []map[string]any{{
						"id": "site", "label": "Site", "type": "select",
						"options": []map[string]any{{"label": "US", "value": "us"}},
					}},
					"variables": map[string]any{
						"host": map[string]any{"from": "site", "map": map[string]string{"us": "us.example"}},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mcpPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	server := &Server{
		output: &output,
		sessions: map[string]*session{
			"sess": {
				id:  "sess",
				cwd: root,
				runner: &agent.Runner{
					PluginInventory: func() []plugin.Plugin {
						return []plugin.Plugin{{Name: "demo", Root: pluginRoot, MCPConfig: mcpPath, Executable: true}}
					},
					MCPServerCatalog: func() []MCPServer { return nil },
				},
			},
		},
	}
	server.handleMCPList(message{ID: json.RawMessage("1"), Method: "x.ai/mcp/list"}, "sess")
	messages := decodeACPOutput(t, output.Bytes())
	result, _ := messages[0]["result"].(map[string]any)
	inner, _ := result["result"].(map[string]any)
	rows, _ := inner["servers"].([]any)
	byName := map[string]map[string]any{}
	for _, raw := range rows {
		row, _ := raw.(map[string]any)
		byName[row["name"].(string)] = row
	}
	acme := byName["plugin-acme"]
	if acme == nil {
		t.Fatalf("rows=%#v", rows)
	}
	if acme["sourceLabel"] != "plugin: demo" {
		t.Fatalf("sourceLabel=%#v", acme["sourceLabel"])
	}
	session, _ := acme["session"].(map[string]any)
	if session["setupRequired"] != true {
		t.Fatalf("session=%#v", session)
	}
}
