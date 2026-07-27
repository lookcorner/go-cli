package acp

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/config"
)

func TestMCPListIncludesSetupRequiredPlaceholders(t *testing.T) {
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

	var output bytes.Buffer
	server := &Server{
		output: &output,
		sessions: map[string]*session{
			"sess": {
				id:  "sess",
				cwd: root,
				runner: &agent.Runner{
					MCPServerCatalog: func() []MCPServer {
						return []MCPServer{{Name: "local", Command: "echo"}}
					},
				},
			},
		},
	}
	server.handleMCPList(message{ID: json.RawMessage("1"), Method: "x.ai/mcp/list"}, "sess")
	messages := decodeACPOutput(t, output.Bytes())
	if len(messages) != 1 {
		t.Fatalf("messages=%#v", messages)
	}
	result, _ := messages[0]["result"].(map[string]any)
	inner, _ := result["result"].(map[string]any)
	rows, _ := inner["servers"].([]any)
	byName := map[string]map[string]any{}
	for _, raw := range rows {
		row, _ := raw.(map[string]any)
		byName[row["name"].(string)] = row
	}
	if byName["local"] == nil {
		t.Fatalf("missing local server: %#v", rows)
	}
	acme := byName["acme"]
	if acme == nil {
		t.Fatalf("missing acme placeholder: %#v", rows)
	}
	session, _ := acme["session"].(map[string]any)
	if session["status"] != "setuprequired" || session["setupRequired"] != true {
		t.Fatalf("session=%#v", session)
	}
	setup, _ := acme["setup"].(map[string]any)
	fields, _ := setup["fields"].([]any)
	if len(fields) != 1 {
		t.Fatalf("setup=%#v", setup)
	}
}
