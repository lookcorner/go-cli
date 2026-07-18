package plugin

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalInstallUpdateDiscoverAndUninstall(t *testing.T) {
	grokHome := filepath.Join(t.TempDir(), ".grok")
	t.Setenv("GROK_HOME", grokHome)
	workspace := t.TempDir()
	source := filepath.Join(workspace, "source")
	mustMkdir(t, filepath.Join(source, "alpha", "skills"))
	mustMkdir(t, filepath.Join(source, "beta", "commands"))
	mustWrite(t, filepath.Join(source, "alpha", "plugin.json"), `{"name":"alpha","version":"1.0.0"}`)
	mustWrite(t, filepath.Join(source, "beta", "plugin.json"), `{"name":"beta"}`)

	installed, err := Install(source, workspace)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(installed.Plugins, "|") != "alpha|beta" {
		t.Fatalf("installed plugins = %#v", installed.Plugins)
	}
	registry, err := LoadInstallRegistry()
	if err != nil {
		t.Fatal(err)
	}
	repo := registry.Repos[installed.RepoKey]
	if repo.Kind.Type != "local" || repo.Kind.SourcePath != canonicalOrClean(source) || repo.Plugins["alpha"].Subdir != "alpha" {
		t.Fatalf("installed repo = %#v", repo)
	}

	mustWrite(t, filepath.Join(source, "alpha", "plugin.json"), `{"name":"alpha","version":"2.0.0"}`)
	if data, err := os.ReadFile(filepath.Join(repo.Path, "alpha", "plugin.json")); err != nil || strings.Contains(string(data), "2.0.0") {
		t.Fatalf("local source leaked into snapshot: %q err=%v", data, err)
	}
	outcomes, err := Update("alpha")
	if err != nil || len(outcomes) != 1 || outcomes[0].Status != "updated" {
		t.Fatalf("update outcomes=%#v err=%v", outcomes, err)
	}
	registry, _ = LoadInstallRegistry()
	if registry.Repos[installed.RepoKey].Plugins["alpha"].Version != "2.0.0" {
		t.Fatalf("updated registry = %#v", registry.Repos[installed.RepoKey])
	}
	mustWrite(t, filepath.Join(source, "alpha", "plugin.json"), `{"name":"alpha","version":"3.0.0"}`)
	if err := RefreshLocal(); err != nil {
		t.Fatal(err)
	}
	registry, _ = LoadInstallRegistry()
	if registry.Repos[installed.RepoKey].Plugins["alpha"].Version != "3.0.0" {
		t.Fatalf("refreshed registry = %#v", registry.Repos[installed.RepoKey])
	}

	inventory, err := discoverInventory(workspace, filepath.Dir(grokHome), grokHome, Config{Enabled: []string{"alpha", "beta"}})
	if err != nil || len(inventory) != 2 || !inventory[0].Enabled || !inventory[1].Enabled {
		t.Fatalf("installed inventory=%#v err=%v", inventory, err)
	}

	if _, err := Uninstall("alpha", false, false); err == nil {
		t.Fatal("multi-plugin uninstall did not require confirmation")
	} else {
		var confirmation *ConfirmationError
		if !errors.As(err, &confirmation) || strings.Join(confirmation.Plugins, "|") != "alpha|beta" {
			t.Fatalf("unexpected confirmation error: %v", err)
		}
	}
	dataRoot := filepath.Join(grokHome, "plugin-data", filepath.FromSlash(pluginID(userScope, filepath.Join(repo.Path, "alpha"), "alpha")))
	mustMkdir(t, dataRoot)
	mustWrite(t, filepath.Join(dataRoot, "state.json"), `{}`)
	removed, err := Uninstall("user/12345678/alpha", true, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(removed.Plugins, "|") != "alpha|beta" {
		t.Fatalf("removed plugins = %#v", removed.Plugins)
	}
	if _, err := os.Stat(repo.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("installed snapshot still exists: %v", err)
	}
	if _, err := os.Stat(dataRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("plugin data still exists: %v", err)
	}
	registry, _ = LoadInstallRegistry()
	if len(registry.Repos) != 0 {
		t.Fatalf("registry was not emptied: %#v", registry.Repos)
	}
}

func TestInstallSubdirAndSkipsSymlinks(t *testing.T) {
	t.Setenv("GROK_HOME", filepath.Join(t.TempDir(), ".grok"))
	workspace := t.TempDir()
	source := filepath.Join(workspace, "source")
	mustMkdir(t, filepath.Join(source, "plugins", "selected", "skills"))
	mustMkdir(t, filepath.Join(source, "plugins", "ignored", "skills"))
	mustWrite(t, filepath.Join(source, "plugins", "selected", "plugin.json"), `{"name":"selected"}`)
	mustWrite(t, filepath.Join(source, "plugins", "ignored", "plugin.json"), `{"name":"ignored"}`)
	outside := filepath.Join(workspace, "secret")
	mustWrite(t, outside, "secret")
	if err := os.Symlink(outside, filepath.Join(source, "plugins", "selected", "secret-link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	installed, err := Install(source+"#plugins/selected", workspace)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(installed.Plugins, "|") != "selected" {
		t.Fatalf("subdir install = %#v", installed.Plugins)
	}
	registry, _ := LoadInstallRegistry()
	repo := registry.Repos[installed.RepoKey]
	if _, err := os.Lstat(filepath.Join(repo.Path, "plugins", "selected", "secret-link")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("symlink was copied: %v", err)
	}
	if _, err := Install(source+"#../escape", workspace); err == nil {
		t.Fatal("escaping plugin subdirectory was accepted")
	}
}

func TestGitInstallAndUpdate(t *testing.T) {
	t.Setenv("GROK_HOME", filepath.Join(t.TempDir(), ".grok"))
	workspace := t.TempDir()
	remote := filepath.Join(workspace, "remote.git")
	working := filepath.Join(workspace, "working")
	runGit(t, workspace, "init", "--bare", remote)
	runGit(t, remote, "symbolic-ref", "HEAD", "refs/heads/main")
	runGit(t, workspace, "init", "-b", "main", working)
	runGit(t, working, "config", "user.email", "test@example.com")
	runGit(t, working, "config", "user.name", "Test")
	mustWrite(t, filepath.Join(working, "plugin.json"), `{"name":"git-plugin","version":"1.0.0"}`)
	runGit(t, working, "add", "plugin.json")
	runGit(t, working, "commit", "-m", "initial")
	runGit(t, working, "remote", "add", "origin", remote)
	runGit(t, working, "push", "-u", "origin", "HEAD")

	installed, err := Install("file://"+remote, workspace)
	if err != nil {
		t.Fatal(err)
	}
	registry, _ := LoadInstallRegistry()
	oldCommit := registry.Repos[installed.RepoKey].Kind.Commit
	mustWrite(t, filepath.Join(working, "plugin.json"), `{"name":"git-plugin","version":"2.0.0"}`)
	runGit(t, working, "add", "plugin.json")
	runGit(t, working, "commit", "-m", "update")
	runGit(t, working, "push")
	outcomes, err := Update("git-plugin")
	if err != nil || len(outcomes) != 1 || outcomes[0].Status != "updated" {
		t.Fatalf("git update=%#v err=%v", outcomes, err)
	}
	registry, _ = LoadInstallRegistry()
	repo := registry.Repos[installed.RepoKey]
	if repo.Kind.Commit == oldCommit || repo.Plugins["git-plugin"].Version != "2.0.0" {
		t.Fatalf("updated git repo=%#v", repo)
	}
}

func TestGitCommitInstallIsPinned(t *testing.T) {
	t.Setenv("GROK_HOME", filepath.Join(t.TempDir(), ".grok"))
	workspace := t.TempDir()
	remote := filepath.Join(workspace, "remote.git")
	working := filepath.Join(workspace, "working")
	runGit(t, workspace, "init", "--bare", remote)
	runGit(t, remote, "symbolic-ref", "HEAD", "refs/heads/main")
	runGit(t, workspace, "init", "-b", "main", working)
	runGit(t, working, "config", "user.email", "test@example.com")
	runGit(t, working, "config", "user.name", "Test")
	mustWrite(t, filepath.Join(working, "plugin.json"), `{"name":"pinned-plugin"}`)
	runGit(t, working, "add", "plugin.json")
	runGit(t, working, "commit", "-m", "initial")
	commit := gitValue(t, working, "rev-parse", "HEAD")
	runGit(t, working, "remote", "add", "origin", remote)
	runGit(t, working, "push", "-u", "origin", "HEAD")
	if _, err := Install("file://"+remote+"@"+commit, workspace); err != nil {
		t.Fatal(err)
	}
	outcomes, err := Update("pinned-plugin")
	if err != nil || len(outcomes) != 1 || outcomes[0].Status != "pinned" {
		t.Fatalf("pinned update=%#v err=%v", outcomes, err)
	}
}

func TestInstallRejectsTargetInsideSource(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GROK_HOME", filepath.Join(root, ".grok"))
	mustWrite(t, filepath.Join(root, "plugin.json"), `{"name":"recursive"}`)
	if _, err := Install(root, root); err == nil || !strings.Contains(err.Error(), "inside the local source") {
		t.Fatalf("recursive install error = %v", err)
	}
}

func runGit(t *testing.T, root string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = root
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
}

func gitValue(t *testing.T, root string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = root
	output, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(output))
}
