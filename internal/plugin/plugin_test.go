package plugin

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscoverConfigPlugin(t *testing.T) {
	workspaceRoot := t.TempDir()
	home := t.TempDir()
	grokHome := filepath.Join(home, ".grok")
	root := filepath.Join(workspaceRoot, "tools")
	mustMkdir(t, filepath.Join(root, "prompts"))
	mustMkdir(t, filepath.Join(root, "commands"))
	mustWrite(t, filepath.Join(root, "plugin.json"), `{"name":"deploy-tools","version":"1.2.3","description":"Deploy helpers","skills":"prompts","commands":["commands"]}`)
	root = canonicalOrClean(root)

	plugins, err := discover(workspaceRoot, home, grokHome, Config{Paths: []string{"tools"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(plugins) != 1 {
		t.Fatalf("plugins = %#v", plugins)
	}
	plugin := plugins[0]
	digest := sha256.Sum256([]byte(root))
	wantID := fmt.Sprintf("config/%x/deploy-tools", digest[:4])
	if plugin.ID != wantID || plugin.Name != "deploy-tools" || plugin.Version != "1.2.3" || plugin.Description != "Deploy helpers" {
		t.Fatalf("plugin = %#v", plugin)
	}
	if strings.Join(plugin.SkillDirs, "|") != filepath.Join(root, "prompts") || strings.Join(plugin.CommandDirs, "|") != filepath.Join(root, "commands") {
		t.Fatalf("component dirs = %#v %#v", plugin.SkillDirs, plugin.CommandDirs)
	}
	if plugin.DataDir != filepath.Join(grokHome, "plugin-data", filepath.FromSlash(wantID)) {
		t.Fatalf("data dir = %q", plugin.DataDir)
	}
}

func TestAutoDiscoveredPluginsRequireEnablement(t *testing.T) {
	workspaceRoot := t.TempDir()
	home := t.TempDir()
	grokHome := filepath.Join(home, ".grok")
	project := filepath.Join(workspaceRoot, ".grok", "plugins", "project-plugin")
	user := filepath.Join(grokHome, "plugins", "user-plugin")
	mustMkdir(t, filepath.Join(project, "skills"))
	mustMkdir(t, filepath.Join(user, "commands"))

	plugins, err := discover(workspaceRoot, home, grokHome, Config{})
	if err != nil || len(plugins) != 0 {
		t.Fatalf("default plugins=%#v err=%v", plugins, err)
	}
	plugins, err = discover(workspaceRoot, home, grokHome, Config{Enabled: []string{"project-plugin", "user-plugin"}})
	if err != nil || len(plugins) != 2 {
		t.Fatalf("enabled plugins=%#v err=%v", plugins, err)
	}
	plugins, err = discover(workspaceRoot, home, grokHome, Config{Enabled: []string{"project-plugin"}, Disabled: []string{"project-plugin"}})
	if err != nil || len(plugins) != 0 {
		t.Fatalf("disabled precedence plugins=%#v err=%v", plugins, err)
	}
}

func TestManifestSearchAndContainedPaths(t *testing.T) {
	workspaceRoot := t.TempDir()
	home := t.TempDir()
	root := filepath.Join(workspaceRoot, "plugin")
	mustMkdir(t, filepath.Join(root, ".grok-plugin"))
	mustMkdir(t, filepath.Join(root, "skills"))
	escape := t.TempDir()
	if err := os.Symlink(escape, filepath.Join(root, "escaped")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	mustWrite(t, filepath.Join(root, ".grok-plugin", "plugin.json"), `{"name":"safe-plugin","skills":["skills","../outside","escaped"]}`)

	plugins, err := discover(workspaceRoot, home, filepath.Join(home, ".grok"), Config{Paths: []string{root}})
	if err != nil || len(plugins) != 1 {
		t.Fatalf("plugins=%#v err=%v", plugins, err)
	}
	if len(plugins[0].SkillDirs) != 1 || plugins[0].SkillDirs[0] != canonicalOrClean(filepath.Join(root, "skills")) {
		t.Fatalf("escaping paths were accepted: %#v", plugins[0].SkillDirs)
	}
}

func TestInvalidManifestAndEmptyConventionDirectoryAreSkipped(t *testing.T) {
	workspaceRoot := t.TempDir()
	home := t.TempDir()
	invalid := filepath.Join(workspaceRoot, "invalid")
	empty := filepath.Join(workspaceRoot, "empty")
	mustMkdir(t, invalid)
	mustMkdir(t, empty)
	mustWrite(t, filepath.Join(invalid, "plugin.json"), `{"name":"Invalid_Name"}`)

	plugins, err := discover(workspaceRoot, home, filepath.Join(home, ".grok"), Config{Paths: []string{invalid, empty}})
	if err != nil || len(plugins) != 0 {
		t.Fatalf("plugins=%#v err=%v", plugins, err)
	}
}

func TestHigherPriorityNameConflictIsResolvedBeforeEnablement(t *testing.T) {
	workspaceRoot := t.TempDir()
	home := t.TempDir()
	project := filepath.Join(workspaceRoot, ".grok", "plugins", "shared")
	configured := filepath.Join(workspaceRoot, "configured")
	mustMkdir(t, filepath.Join(project, "skills"))
	mustMkdir(t, filepath.Join(configured, "skills"))
	mustWrite(t, filepath.Join(configured, "plugin.json"), `{"name":"shared"}`)

	plugins, err := discover(workspaceRoot, home, filepath.Join(home, ".grok"), Config{Paths: []string{configured}})
	if err != nil || len(plugins) != 0 {
		t.Fatalf("lower-priority configured plugin bypassed project name conflict: plugins=%#v err=%v", plugins, err)
	}
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
