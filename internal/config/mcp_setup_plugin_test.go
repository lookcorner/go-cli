package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/lookcorner/go-cli/internal/plugin"
)

func TestCollectMCPSetupConfigsIncludesPluginSources(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(home, "config.toml")
	enabled := true
	if err := UpsertMCPServer(cfgPath, "user-acme", MCPServerConfig{
		URL: "https://{{host}}/mcp", Enabled: &enabled,
		Setup: &MCPSetupConfig{
			Fields: []MCPSetupField{{
				ID: "site", Label: "Site", Type: "select",
				Options: []MCPSetupOption{{Label: "US", Value: "us"}},
			}},
			Variables: map[string]MCPSetupDerivedValue{
				"host": {From: "site", Map: map[string]string{"us": "us.example"}},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	pluginRoot := t.TempDir()
	mcpPath := filepath.Join(pluginRoot, ".mcp.json")
	raw, _ := json.Marshal(map[string]any{
		"mcpServers": map[string]any{
			"plugin-acme": map[string]any{
				"url": "https://{{host}}/plugin",
				"setup": map[string]any{
					"fields": []map[string]any{{
						"id": "site", "label": "Site", "type": "select",
						"options": []map[string]any{{"label": "US", "value": "us"}},
					}},
					"variables": map[string]any{
						"host": map[string]any{"from": "site", "map": map[string]string{"us": "plugin.example"}},
					},
				},
			},
			"user-acme": map[string]any{ // claimed by config — skipped
				"url": "https://ignored",
				"setup": map[string]any{
					"fields": []map[string]any{{
						"id": "site", "label": "Site", "type": "select",
						"options": []map[string]any{{"label": "US", "value": "us"}},
					}},
				},
			},
		},
	})
	if err := os.WriteFile(mcpPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	entries := CollectMCPSetupConfigs(root, cfg, []plugin.Plugin{{
		Name: "demo-plugin", Root: pluginRoot, MCPConfig: mcpPath, Executable: true,
	}}, true)
	if entries["user-acme"].Source.Kind != "config" || entries["user-acme"].Source.Plugin != nil {
		t.Fatalf("user-acme=%#v", entries["user-acme"])
	}
	pluginEntry := entries["plugin-acme"]
	if pluginEntry.Source.Kind != "plugin" || pluginEntry.Source.Plugin == nil || *pluginEntry.Source.Plugin != "demo-plugin" {
		t.Fatalf("plugin-acme=%#v", pluginEntry)
	}
	if pluginEntry.Config.Setup == nil || pluginEntry.Config.URL != "https://{{host}}/plugin" {
		t.Fatalf("plugin config=%#v", pluginEntry.Config)
	}
}

func TestCollectMCPSetupConfigsPluginOnlyNoConfigFile(t *testing.T) {
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
	path, err := DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	entries := CollectMCPSetupConfigs(root, cfg, []plugin.Plugin{{
		Name: "demo", Root: pluginRoot, MCPConfig: mcpPath, Executable: true,
	}}, true)
	if entries["plugin-acme"].Source.Kind != "plugin" {
		t.Fatalf("entries=%#v", entries)
	}
}
