package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestResolveSetupSelectField(t *testing.T) {
	server := MCPServerConfig{
		URL: "https://{{host}}/mcp",
		Setup: &MCPSetupConfig{
			Fields: []MCPSetupField{{
				ID: "site", Label: "Site", Type: "select",
				Options: []MCPSetupOption{{Label: "US", Value: "us"}, {Label: "EU", Value: "eu"}},
			}},
			Variables: map[string]MCPSetupDerivedValue{
				"host": {From: "site", Map: map[string]string{"us": "us.example", "eu": "eu.example"}},
			},
		},
	}
	if got := server.ResolveSetup(nil); got.Kind != MCPSetupRequired {
		t.Fatalf("nil prefs=%#v", got)
	}
	prefs := MCPServerPreferences{Values: map[string]string{"site": "eu"}}
	got := server.ResolveSetup(&prefs)
	if got.Kind != MCPSetupResolved || got.Config.URL != "https://eu.example/mcp" || got.Config.Setup != nil {
		t.Fatalf("resolved=%#v", got)
	}
}

func TestMCPPreferencesRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	path := filepath.Join(home, "mcp_preferences.json")
	if LoadMCPPreferencesAt(path).Status != MCPPreferencesMissing {
		t.Fatal("expected missing")
	}
	prefs := emptyMCPPreferences()
	prefs.Servers["acme"] = NewMCPServerPreferences(map[string]string{"site": "us"}, &MCPPreferenceSource{Kind: "config"})
	if err := SaveMCPPreferencesAt(path, prefs); err != nil {
		t.Fatal(err)
	}
	loaded := LoadMCPPreferencesAt(path)
	if loaded.Status != MCPPreferencesOK || loaded.File.Servers["acme"].Values["site"] != "us" {
		t.Fatalf("loaded=%#v", loaded)
	}
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if LoadMCPPreferencesAt(path).Writable() {
		t.Fatal("corrupt should not be writable")
	}
	if err := SaveMCPPreferencesAt(path, prefs); err == nil {
		t.Fatal("expected refuse overwrite")
	}
}

func TestApplyMCPSetupPreferences(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	prefs := emptyMCPPreferences()
	prefs.Servers["acme"] = NewMCPServerPreferences(map[string]string{"site": "us"}, nil)
	if err := SaveMCPPreferences(prefs); err != nil {
		t.Fatal(err)
	}
	servers := map[string]MCPServerConfig{
		"acme": {
			URL: "https://{{host}}/mcp",
			Setup: &MCPSetupConfig{
				Fields: []MCPSetupField{{
					ID: "site", Label: "Site", Type: "select",
					Options: []MCPSetupOption{{Label: "US", Value: "us"}},
				}},
				Variables: map[string]MCPSetupDerivedValue{
					"host": {From: "site", Map: map[string]string{"us": "us.example"}},
				},
			},
		},
	}
	applied := ApplyMCPSetupPreferences(servers)
	if applied["acme"].NeedsSetup() || applied["acme"].URL != "https://us.example/mcp" {
		t.Fatalf("applied=%#v", applied["acme"])
	}
}

func TestMCPSetupConfigJSONTags(t *testing.T) {
	raw := []byte(`{"url":"https://{{host}}/mcp","setup":{"fields":[{"id":"site","label":"Site","type":"select","options":[{"label":"US","value":"us"}]}],"variables":{"host":{"from":"site","map":{"us":"us.example"}}}}}`)
	var server MCPServerConfig
	if err := json.Unmarshal(raw, &server); err != nil {
		t.Fatal(err)
	}
	if server.Setup == nil || len(server.Setup.Fields) != 1 || server.Setup.Fields[0].Type != "select" {
		t.Fatalf("server=%#v", server)
	}
}
