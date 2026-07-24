package plugin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateManifestAndConventionDirectory(t *testing.T) {
	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, "skills", "one"))
	mustMkdir(t, filepath.Join(root, "commands", "one"))
	mustWrite(t, filepath.Join(root, "hooks.json"), "{}")
	mustWrite(t, filepath.Join(root, "plugin.json"), `{
		"name":"fixture-plugin",
		"version":"1.2.3",
		"description":"Fixture",
		"skills":"skills",
		"commands":["commands"],
		"hooks":"hooks.json",
		"mcpServers":{"fixture":{"command":"echo"}}
	}`)
	result, err := Validate(root)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Found || result.Name != "fixture-plugin" || result.Version != "1.2.3" ||
		result.Components.SkillDirs != 1 || result.Components.CommandDirs != 1 ||
		!result.Components.Hooks || !result.Components.MCP || result.Components.LSP {
		t.Fatalf("validation=%#v", result)
	}

	convention := t.TempDir()
	mustMkdir(t, filepath.Join(convention, "skills"))
	if result, err := Validate(convention); err != nil || result.Found {
		t.Fatalf("convention validation=%#v err=%v", result, err)
	}

	fallback := t.TempDir()
	mustMkdir(t, filepath.Join(fallback, ".grok-plugin"))
	mustWrite(t, filepath.Join(fallback, ".grok-plugin", "plugin.json"), `{"name":"fallback-plugin"}`)
	if result, err := Validate(fallback); err != nil || !result.Found || result.Name != "fallback-plugin" {
		t.Fatalf("fallback validation=%#v err=%v", result, err)
	}
}

func TestValidateRejectsMalformedAndInvalidManifests(t *testing.T) {
	for _, test := range []struct {
		name string
		data string
		want string
	}{
		{name: "malformed", data: `{"name":`, want: "parse plugin manifest"},
		{name: "invalid-name", data: `{"name":"Invalid Name"}`, want: "manifest validation failed"},
		{name: "invalid-component", data: `{"name":"valid","skills":42}`, want: "plugin component path"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "plugin.json"), []byte(test.data), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Validate(root); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestListInstalledIsStableAndIncludesProvenance(t *testing.T) {
	t.Setenv("GROK_HOME", t.TempDir())
	registry, err := LoadInstallRegistry()
	if err != nil {
		t.Fatal(err)
	}
	registry.Repos["z-repo"] = InstalledRepo{
		Kind: InstallKind{Type: "git", URL: "https://example.com/z.git"},
		Path: "/plugins/z",
		Plugins: map[string]RepoPlugin{
			"zeta":  {Version: "2.0.0"},
			"alpha": {Version: "1.0.0"},
		},
	}
	registry.Repos["a-repo"] = InstalledRepo{
		Kind: InstallKind{Type: "local", SourcePath: "/source/a"},
		Path: "/plugins/a",
		Plugins: map[string]RepoPlugin{
			"middle": {Version: "3.0.0"},
		},
		Marketplace: &MarketplaceProvenance{SourceDisplayName: "Team catalog"},
	}
	if err := registry.Save(); err != nil {
		t.Fatal(err)
	}
	entries, err := ListInstalled()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 ||
		entries[0].RepoKey != "a-repo" || entries[0].Name != "middle" ||
		entries[0].Source != "/source/a" || entries[0].Marketplace != "Team catalog" ||
		entries[1].RepoKey != "z-repo" || entries[1].Name != "alpha" ||
		entries[2].RepoKey != "z-repo" || entries[2].Name != "zeta" {
		t.Fatalf("entries=%#v", entries)
	}
}
