package marketplace

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/lookcorner/go-cli/internal/config"
	"github.com/lookcorner/go-cli/internal/plugin"
)

func TestLocalMarketplaceLifecycle(t *testing.T) {
	grokHome := filepath.Join(t.TempDir(), ".grok")
	t.Setenv("GROK_HOME", grokHome)
	root := t.TempDir()
	pluginRoot := filepath.Join(root, "plugins", "demo")
	mustMkdir(t, filepath.Join(pluginRoot, "skills", "review"))
	mustWrite(t, filepath.Join(pluginRoot, "plugin.json"), `{"name":"demo","version":"1.0.0","description":"Demo"}`)
	mustWrite(t, filepath.Join(pluginRoot, "skills", "review", "SKILL.md"), "---\nname: review\ndescription: Review\n---\nReview")
	configPath := filepath.Join(grokHome, "config.toml")
	if err := config.UpdateMarketplace(configPath, func(settings *config.MarketplaceConfig) {
		settings.Sources = []config.MarketplaceSourceConfig{{Name: "Local", Path: root}}
	}); err != nil {
		t.Fatal(err)
	}
	results, err := List(configPath, t.TempDir())
	if err != nil || len(results) != 1 || len(results[0].Plugins) != 1 {
		t.Fatalf("marketplace results=%#v err=%v", results, err)
	}
	entry := results[0].Plugins[0]
	if entry.Name != "demo" || entry.Version != "1.0.0" || entry.SkillCount != 1 || entry.InstallStatus != "not_installed" {
		t.Fatalf("marketplace entry=%#v", entry)
	}
	action := Action{Type: "install", SourceURLOrPath: root, PluginRelativePath: "plugins/demo"}
	outcome, err := Execute(configPath, root, action)
	if err != nil || outcome.Status != "success" || strings.Join(outcome.Plugins, "|") != "demo" {
		t.Fatalf("install outcome=%#v err=%v", outcome, err)
	}
	results, _ = List(configPath, root)
	if results[0].Plugins[0].InstallStatus != "installed" {
		t.Fatalf("installed marketplace entry=%#v", results[0].Plugins[0])
	}
	mustWrite(t, filepath.Join(pluginRoot, "plugin.json"), `{"name":"demo","version":"2.0.0"}`)
	outcome, err = Execute(configPath, root, Action{Type: "update", SourceURLOrPath: root, PluginRelativePath: "plugins/demo"})
	if err != nil || outcome.Status != "success" {
		t.Fatalf("update outcome=%#v err=%v", outcome, err)
	}
	registry, _ := plugin.LoadInstallRegistry()
	for _, repo := range registry.Repos {
		if repo.Plugins["demo"].Version != "2.0.0" {
			t.Fatalf("updated marketplace repo=%#v", repo)
		}
	}
	outcome, err = Execute(configPath, root, Action{Type: "uninstall", SourceURLOrPath: root, PluginRelativePath: "plugins/demo"})
	if err != nil || outcome.Status != "success" || strings.Join(outcome.Plugins, "|") != "demo" {
		t.Fatalf("uninstall outcome=%#v err=%v", outcome, err)
	}
	registry, _ = plugin.LoadInstallRegistry()
	if len(registry.Repos) != 0 {
		t.Fatalf("marketplace registry not empty: %#v", registry.Repos)
	}
}

func TestMarketplaceIndexAndTraversal(t *testing.T) {
	t.Setenv("GROK_HOME", filepath.Join(t.TempDir(), ".grok"))
	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, ".grok-plugin"))
	mustMkdir(t, filepath.Join(root, "plugins", "valid", "skills", "one"))
	mustWrite(t, filepath.Join(root, "plugins", "valid", "plugin.json"), `{"name":"valid","version":"1.0.0"}`)
	mustWrite(t, filepath.Join(root, "plugins", "valid", "skills", "one", "SKILL.md"), "one")
	mustWrite(t, filepath.Join(root, ".grok-plugin", "marketplace.json"), `{
  "name":"Fixture","plugins":[
    {"name":"valid","category":"dev","author":{"name":"Team"},"source":"plugins/valid"},
    {"name":"escape","source":{"path":"../outside"}},
    {"name":"remote","version":"3.0.0","source":{"source":"url","url":"https://example.com/remote.git","sha":"0123456789012345678901234567890123456789","path":"plugin"}}
  ]
}`)
	entries := scanRoot(root, root)
	if len(entries) != 2 || entries[0].Name != "valid" || entries[0].Author != "Team" || entries[0].SkillCount != 1 || entries[1].RemoteURL == "" || entries[1].RemoteSubdir != "plugin" {
		t.Fatalf("indexed entries=%#v", entries)
	}
	if _, err := safeJoin(root, "../escape"); err == nil {
		t.Fatal("marketplace traversal path was accepted")
	}
}

func TestRemoteMarketplaceSHAUpdate(t *testing.T) {
	grokHome := filepath.Join(t.TempDir(), ".grok")
	t.Setenv("GROK_HOME", grokHome)
	workspace := t.TempDir()
	remote := filepath.Join(workspace, "remote.git")
	working := filepath.Join(workspace, "working")
	runGit(t, workspace, "init", "--bare", remote)
	runGit(t, remote, "symbolic-ref", "HEAD", "refs/heads/main")
	runGit(t, workspace, "init", "-b", "main", working)
	runGit(t, working, "config", "user.email", "test@example.com")
	runGit(t, working, "config", "user.name", "Test")
	mustWrite(t, filepath.Join(working, "plugin.json"), `{"name":"remote-plugin","version":"1.0.0"}`)
	runGit(t, working, "add", "plugin.json")
	runGit(t, working, "commit", "-m", "initial")
	firstSHA := gitValue(t, working, "rev-parse", "HEAD")
	runGit(t, working, "remote", "add", "origin", remote)
	runGit(t, working, "push", "-u", "origin", "HEAD")

	catalog := filepath.Join(workspace, "catalog")
	mustMkdir(t, filepath.Join(catalog, ".grok-plugin"))
	writeRemoteIndex(t, catalog, "file://"+remote, firstSHA)
	configPath := filepath.Join(grokHome, "config.toml")
	if err := config.UpdateMarketplace(configPath, func(settings *config.MarketplaceConfig) {
		settings.Sources = []config.MarketplaceSourceConfig{{Name: "Remote index", Path: catalog}}
	}); err != nil {
		t.Fatal(err)
	}
	action := Action{Type: "install", SourceURLOrPath: catalog, PluginRelativePath: "remote-plugin"}
	outcome, err := Execute(configPath, workspace, action)
	if err != nil || outcome.Status != "success" {
		t.Fatalf("remote install=%#v err=%v", outcome, err)
	}
	registry, _ := plugin.LoadInstallRegistry()
	var repoKey string
	for key, repo := range registry.Repos {
		repoKey = key
		if repo.Kind.Ref != firstSHA || repo.Plugins["remote-plugin"].Version != "1.0.0" {
			t.Fatalf("initial remote repo=%#v", repo)
		}
	}
	mustWrite(t, filepath.Join(working, "plugin.json"), `{"name":"remote-plugin","version":"2.0.0"}`)
	runGit(t, working, "add", "plugin.json")
	runGit(t, working, "commit", "-m", "update")
	secondSHA := gitValue(t, working, "rev-parse", "HEAD")
	runGit(t, working, "push")
	writeRemoteIndex(t, catalog, "file://"+remote, secondSHA)
	outcome, err = Execute(configPath, workspace, Action{Type: "update", SourceURLOrPath: catalog, PluginRelativePath: "remote-plugin"})
	if err != nil || outcome.Status != "success" {
		t.Fatalf("remote update=%#v err=%v", outcome, err)
	}
	registry, _ = plugin.LoadInstallRegistry()
	repo := registry.Repos[repoKey]
	if repo.Kind.Ref != secondSHA || repo.Plugins["remote-plugin"].Version != "2.0.0" {
		t.Fatalf("updated remote repo=%#v", repo)
	}
}

func TestGitMarketplaceSourceRefresh(t *testing.T) {
	grokHome := filepath.Join(t.TempDir(), ".grok")
	t.Setenv("GROK_HOME", grokHome)
	workspace := t.TempDir()
	remote := filepath.Join(workspace, "catalog.git")
	working := filepath.Join(workspace, "catalog")
	runGit(t, workspace, "init", "--bare", remote)
	runGit(t, remote, "symbolic-ref", "HEAD", "refs/heads/main")
	runGit(t, workspace, "init", "-b", "main", working)
	runGit(t, working, "config", "user.email", "test@example.com")
	runGit(t, working, "config", "user.name", "Test")
	mustMkdir(t, filepath.Join(working, "plugins", "demo"))
	mustWrite(t, filepath.Join(working, "plugins", "demo", "plugin.json"), `{"name":"git-market","version":"1.0.0"}`)
	runGit(t, working, "add", ".")
	runGit(t, working, "commit", "-m", "initial")
	runGit(t, working, "remote", "add", "origin", remote)
	runGit(t, working, "push", "-u", "origin", "HEAD")
	configPath := filepath.Join(grokHome, "config.toml")
	if err := config.UpdateMarketplace(configPath, func(settings *config.MarketplaceConfig) {
		settings.Sources = []config.MarketplaceSourceConfig{{Name: "Git", Git: "file://" + remote, Branch: "main"}}
	}); err != nil {
		t.Fatal(err)
	}
	results, err := List(configPath, workspace)
	if err != nil || len(results) != 1 || results[0].SourceKind != "git" || results[0].Plugins[0].Version != "1.0.0" {
		t.Fatalf("git source results=%#v err=%v", results, err)
	}
	mustWrite(t, filepath.Join(working, "plugins", "demo", "plugin.json"), `{"name":"git-market","version":"2.0.0"}`)
	runGit(t, working, "add", ".")
	runGit(t, working, "commit", "-m", "update")
	runGit(t, working, "push")
	if outcome, err := Execute(configPath, workspace, Action{Type: "refresh", SourceURLOrPath: "file://" + remote}); err != nil || outcome.Status != "success" {
		t.Fatalf("git source refresh=%#v err=%v", outcome, err)
	}
	results, err = List(configPath, workspace)
	if err != nil || results[0].Plugins[0].Version != "2.0.0" {
		t.Fatalf("refreshed git source=%#v err=%v", results, err)
	}
}

func TestAddAndRemoveMarketplaceSource(t *testing.T) {
	grokHome := filepath.Join(t.TempDir(), ".grok")
	t.Setenv("GROK_HOME", grokHome)
	configPath := filepath.Join(grokHome, "config.toml")
	cwd := t.TempDir()
	mustMkdir(t, filepath.Join(cwd, "catalog", "plugins", "demo"))
	mustWrite(t, filepath.Join(cwd, "catalog", "plugins", "demo", "plugin.json"), `{"name":"demo","version":"1.0.0"}`)
	outcome, err := Execute(configPath, cwd, Action{Type: "add_source", SourceURLOrPath: "./catalog"})
	if err != nil || outcome.Status != "success" {
		t.Fatalf("add source=%#v err=%v", outcome, err)
	}
	sources, err := Sources(configPath, cwd)
	if err != nil || len(sources) != 1 || sources[0].Path != filepath.Join(cwd, "catalog") {
		t.Fatalf("sources=%#v err=%v", sources, err)
	}
	if duplicate, err := Execute(configPath, cwd, Action{Type: "add_source", SourceURLOrPath: "./catalog"}); err != nil || duplicate.Status != "validation_error" {
		t.Fatalf("duplicate source=%#v err=%v", duplicate, err)
	}
	if installed, err := Execute(configPath, cwd, Action{Type: "install", SourceURLOrPath: sources[0].Path, PluginRelativePath: "plugins/demo"}); err != nil || installed.Status != "success" {
		t.Fatalf("install source plugin=%#v err=%v", installed, err)
	}
	if err := config.UpdatePlugins(configPath, func(settings *config.PluginsConfig) { settings.Enabled = []string{"demo"} }); err != nil {
		t.Fatal(err)
	}
	outcome, err = Execute(configPath, cwd, Action{Type: "remove_source", SourceURLOrPath: sources[0].Path})
	if err != nil || outcome.Status != "success" || strings.Join(outcome.Plugins, "|") != "demo" {
		t.Fatalf("remove source=%#v err=%v", outcome, err)
	}
	sources, _ = Sources(configPath, cwd)
	if len(sources) != 0 {
		t.Fatalf("source was not removed: %#v", sources)
	}
	registry, _ := plugin.LoadInstallRegistry()
	cfg, _ := config.Load(configPath)
	if len(registry.Repos) != 0 || len(cfg.Plugins.Enabled) != 0 {
		t.Fatalf("source plugin not removed: registry=%#v plugins=%#v", registry.Repos, cfg.Plugins)
	}
	if missing, err := Execute(configPath, cwd, Action{Type: "remove_source", SourceURLOrPath: "./catalog"}); err != nil || missing.Status != "not_found" {
		t.Fatalf("missing source=%#v err=%v", missing, err)
	}
	if added, err := Execute(configPath, cwd, Action{Type: "add_source", SourceURLOrPath: "owner/catalog"}); err != nil || added.Status != "success" {
		t.Fatalf("github shorthand=%#v err=%v", added, err)
	}
	sources, _ = Sources(configPath, cwd)
	if len(sources) != 1 || sources[0].Git != "https://github.com/owner/catalog.git" {
		t.Fatalf("normalized source=%#v", sources)
	}
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, path, value string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeRemoteIndex(t *testing.T, root, remote, sha string) {
	t.Helper()
	mustWrite(t, filepath.Join(root, ".grok-plugin", "marketplace.json"), `{"name":"Remote","plugins":[{"name":"remote-plugin","source":{"source":"url","url":`+strconv.Quote(remote)+`,"sha":`+strconv.Quote(sha)+`}}]}`)
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
