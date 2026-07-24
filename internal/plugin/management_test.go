package plugin

import (
	"os"
	"os/exec"
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

func TestTagCreatesDryRunsForcesAndPushesVersionTag(t *testing.T) {
	root := t.TempDir()
	tagGit(t, root, "init", "-b", "main")
	tagGit(t, root, "config", "user.email", "test@example.com")
	tagGit(t, root, "config", "user.name", "Test")
	mustWrite(t, filepath.Join(root, "plugin.json"), `{"name":"tag-plugin","version":"V1.2.3"}`)
	tagGit(t, root, "add", "plugin.json")
	tagGit(t, root, "commit", "-m", "initial")

	result, err := Tag(root, true, false, true)
	if err != nil || result.Tag != "v1.2.3" || !result.DryRun || !result.Push || result.Created || result.Pushed {
		t.Fatalf("dry-run result=%#v err=%v", result, err)
	}
	if output := tagGitOutput(t, root, "tag", "--list"); output != "" {
		t.Fatalf("dry-run created tag %q", output)
	}

	result, err = Tag(root, false, false, false)
	if err != nil || !result.Created || result.Pushed || tagGitOutput(t, root, "tag", "--list") != "v1.2.3" {
		t.Fatalf("create result=%#v tags=%q err=%v", result, tagGitOutput(t, root, "tag", "--list"), err)
	}
	if _, err := Tag(root, false, false, false); err == nil || !strings.Contains(err.Error(), "failed to create tag") {
		t.Fatalf("existing tag error=%v", err)
	}

	mustWrite(t, filepath.Join(root, "dirty.txt"), "dirty")
	if _, err := Tag(root, false, false, false); err == nil || !strings.Contains(err.Error(), "working tree is dirty") {
		t.Fatalf("dirty tree error=%v", err)
	}
	if result, err = Tag(root, false, true, false); err != nil || !result.Created {
		t.Fatalf("forced result=%#v err=%v", result, err)
	}

	remote := filepath.Join(t.TempDir(), "remote.git")
	tagGit(t, filepath.Dir(remote), "init", "--bare", remote)
	tagGit(t, root, "remote", "add", "origin", remote)
	if result, err = Tag(root, true, true, false); err != nil || !result.Pushed {
		t.Fatalf("push result=%#v err=%v", result, err)
	}
	if output := tagGitOutput(t, remote, "show-ref", "--tags"); !strings.Contains(output, "refs/tags/v1.2.3") {
		t.Fatalf("remote tags=%q", output)
	}
}

func TestTagRejectsInvalidPluginDirectories(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	if _, err := Tag(missing, false, false, false); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("missing directory error=%v", err)
	}
	root := t.TempDir()
	tagGit(t, root, "init")
	if _, err := Tag(root, false, false, false); err == nil || !strings.Contains(err.Error(), "no plugin.json") {
		t.Fatalf("missing manifest error=%v", err)
	}
	mustWrite(t, filepath.Join(root, "plugin.json"), `{"name":"tag-plugin"}`)
	if _, err := Tag(root, false, false, false); err == nil || !strings.Contains(err.Error(), "version") {
		t.Fatalf("missing version error=%v", err)
	}
}

func tagGit(t *testing.T, root string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = root
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}

func tagGitOutput(t *testing.T, root string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = root
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}
