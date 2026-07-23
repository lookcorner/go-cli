package tui

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/hooks"
	"github.com/lookcorner/go-cli/internal/marketplace"
	"github.com/lookcorner/go-cli/internal/plugin"
	"github.com/lookcorner/go-cli/internal/skills"
)

func TestExtensionCommandsOpenCorrectTabsAndSearch(t *testing.T) {
	runner := &agent.Runner{PluginInventory: func() []plugin.Plugin { return []plugin.Plugin{{ID: "demo", Name: "Demo", Enabled: true}} }}
	for command, want := range map[string]extensionsTab{"/hooks": extensionsHooks, "/plugins": extensionsPlugins, "/marketplace": extensionsMarketplace, "/skills": extensionsSkills} {
		m := &model{width: 80, height: 20, runner: runner}
		m.setInput(command)
		updated, cmd := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
		m = updated.(*model)
		if m.extensions == nil || m.extensions.tab != want {
			t.Fatalf("command=%s state=%#v", command, m.extensions)
		}
		if want != extensionsMarketplace && cmd != nil {
			t.Fatalf("command=%s unexpectedly asynchronous", command)
		}
	}

	m := &model{runner: runner, extensions: &extensionsState{tab: extensionsPlugins}}
	updated, _ := m.handleExtensionsKey(tea.KeyPressMsg(tea.Key{Code: '/', Text: "/"}))
	m = updated.(*model)
	updated, _ = m.handleExtensionsKey(tea.KeyPressMsg(tea.Key{Code: 'd', Text: "d"}))
	m = updated.(*model)
	updated, _ = m.handleExtensionsKey(tea.KeyPressMsg(tea.Key{Code: 'm', Text: "m"}))
	m = updated.(*model)
	if string(m.extensions.query) != "dm" || len(m.extensionRows()) != 1 {
		t.Fatalf("query=%q rows=%#v", m.extensions.query, m.extensionRows())
	}
	updated, _ = m.handleExtensionsKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyRight}))
	if updated.(*model).extensions.tab != extensionsPlugins {
		t.Fatal("search mode leaked tab navigation")
	}
}

func TestExtensionReloadReportsUnavailableCallbacks(t *testing.T) {
	for _, tab := range []extensionsTab{extensionsHooks, extensionsPlugins, extensionsSkills} {
		m := &model{runner: &agent.Runner{}, extensions: &extensionsState{tab: tab}}
		updated, cmd := m.handleExtensionsKey(tea.KeyPressMsg(tea.Key{Code: 'r', Text: "r"}))
		m = updated.(*model)
		if cmd == nil {
			t.Fatalf("tab=%d reload was synchronous", tab)
		}
		updated, _ = m.handleExtensionsEvent(cmd().(extensionsEvent))
		if updated.(*model).extensions.err == "" {
			t.Fatalf("tab=%d missing reload error", tab)
		}
	}
}

func TestExtensionsToggleHooksPluginsAndSkills(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	t.Setenv("GROK_HOME", home)
	hookCatalog := hooks.DiscoverPlugins([]plugin.Plugin{{Name: "fixture", Root: root, Executable: true, InlineHooks: json.RawMessage(`{"hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"check"}]}]}}`)}})
	skillPath := filepath.Join(root, ".grok", "skills", "review", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skillPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skillPath, []byte("---\nname: review\ndescription: Review code\n---\nBody"), 0o600); err != nil {
		t.Fatal(err)
	}
	skillCatalog, err := skills.Discover(root, skills.Config{})
	if err != nil {
		t.Fatal(err)
	}
	pluginSettings, skillSettings := plugin.Settings{}, skills.Settings{}
	runner := &agent.Runner{
		HookCatalog: hookCatalog, Skills: skillCatalog,
		PluginInventory: func() []plugin.Plugin { return []plugin.Plugin{{ID: "demo", Name: "Demo", Enabled: true}} },
		UpdatePlugins: func(_ context.Context, update func(*plugin.Settings)) ([]plugin.Plugin, error) {
			update(&pluginSettings)
			return nil, nil
		},
		UpdateSkills: func(_ context.Context, update func(*skills.Settings)) (skills.Settings, error) {
			update(&skillSettings)
			return skillSettings, nil
		},
	}
	for tab := extensionsHooks; tab <= extensionsSkills; tab++ {
		if tab == extensionsMarketplace {
			continue
		}
		m := &model{runner: runner, extensions: &extensionsState{tab: tab}}
		if len(m.extensionRows()) != 1 {
			t.Fatalf("tab=%d rows=%#v", tab, m.extensionRows())
		}
		updated, cmd := m.handleExtensionsKey(tea.KeyPressMsg(tea.Key{Code: tea.KeySpace}))
		m = updated.(*model)
		if cmd == nil {
			t.Fatalf("tab=%d toggle was synchronous", tab)
		}
		event := cmd().(extensionsEvent)
		updated, _ = m.handleExtensionsEvent(event)
		if updated.(*model).extensions.err != "" {
			t.Fatalf("tab=%d err=%q", tab, updated.(*model).extensions.err)
		}
	}
	if !hookCatalog.Snapshot().Hooks[0].Disabled {
		t.Fatal("hook was not disabled")
	}
	if strings.Join(pluginSettings.Disabled, ",") != "demo" || len(pluginSettings.Enabled) != 0 {
		t.Fatalf("plugin settings=%#v", pluginSettings)
	}
	if strings.Join(skillSettings.Disabled, ",") != "review" {
		t.Fatalf("skill settings=%#v", skillSettings)
	}
}

func TestExtensionSkillsUseQualifiedPluginNames(t *testing.T) {
	root := t.TempDir()
	nativePath := filepath.Join(root, ".grok", "skills", "review", "SKILL.md")
	pluginPath := filepath.Join(root, "plugin", "skills", "review", "SKILL.md")
	for _, path := range []string{nativePath, pluginPath} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("---\nname: review\ndescription: Review code\n---\nBody"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	catalog, err := skills.Discover(root, skills.Config{Plugins: []plugin.Plugin{{Name: "fixture", SkillDirs: []string{filepath.Dir(pluginPath)}}}})
	if err != nil {
		t.Fatal(err)
	}
	m := &model{runner: &agent.Runner{Skills: catalog}, extensions: &extensionsState{tab: extensionsSkills}}
	rows := m.extensionRows()
	keys := make(map[string]bool, len(rows))
	for _, row := range rows {
		keys[row.key] = true
	}
	if len(rows) != 2 || !keys["review"] || !keys["fixture:review"] {
		t.Fatalf("skill rows=%#v", rows)
	}
}

func TestMarketplaceLoadsActionsAndConfirmsUninstall(t *testing.T) {
	var actions []string
	runner := &agent.Runner{
		MarketplaceList: func() ([]marketplace.ScanResult, error) {
			return []marketplace.ScanResult{{SourceName: "Local", SourceURLOrPath: "/catalog", Plugins: []marketplace.Entry{{Name: "demo", RelativePath: "plugins/demo", InstallStatus: "installed"}}}}, nil
		},
		MarketplaceAction: func(_ context.Context, action marketplace.Action) (marketplace.Outcome, error) {
			actions = append(actions, action.Type)
			return marketplace.Outcome{Status: "success", Message: action.Type + "d"}, nil
		},
	}
	m := &model{runner: runner}
	cmd := m.openExtensions("marketplace")
	if cmd == nil {
		t.Fatal("marketplace did not load asynchronously")
	}
	updated, _ := m.handleExtensionsEvent(cmd().(extensionsEvent))
	m = updated.(*model)
	if len(m.extensionRows()) != 1 || m.extensions.busy {
		t.Fatalf("rows=%#v state=%#v", m.extensionRows(), m.extensions)
	}
	updated, cmd = m.handleExtensionsKey(tea.KeyPressMsg(tea.Key{Code: 'i', Text: "i"}))
	m = updated.(*model)
	if cmd != nil || m.extensions.err != "Plugin is already installed" {
		t.Fatalf("invalid install state=%#v", m.extensions)
	}
	m.extensions.err = ""
	updated, cmd = m.handleExtensionsKey(tea.KeyPressMsg(tea.Key{Code: 'u', Text: "u"}))
	m = updated.(*model)
	if cmd == nil || cmd().(extensionsEvent).err != nil || strings.Join(actions, ",") != "update" {
		t.Fatalf("update action=%v state=%#v", actions, m.extensions)
	}
	m.extensions.busy = false
	updated, cmd = m.handleExtensionsKey(tea.KeyPressMsg(tea.Key{Code: 'd', Text: "d"}))
	m = updated.(*model)
	if cmd != nil || m.extensions.confirm == nil || strings.Join(actions, ",") != "update" {
		t.Fatal("uninstall bypassed confirmation")
	}
	updated, cmd = m.handleExtensionsKey(tea.KeyPressMsg(tea.Key{Code: 'y', Text: "y"}))
	m = updated.(*model)
	if cmd == nil {
		t.Fatal("confirmed uninstall did not run")
	}
	updated, next := m.handleExtensionsEvent(cmd().(extensionsEvent))
	m = updated.(*model)
	if strings.Join(actions, ",") != "update,uninstall" || next == nil {
		t.Fatalf("actions=%v refresh=%v", actions, next != nil)
	}
	m.extensions.busy = false
	m.extensions.marketplace[0].Plugins[0].InstallStatus = "not_installed"
	updated, cmd = m.handleExtensionsKey(tea.KeyPressMsg(tea.Key{Code: 'u', Text: "u"}))
	m = updated.(*model)
	if cmd != nil || m.extensions.err != "Plugin is not installed" {
		t.Fatalf("invalid update state=%#v", m.extensions)
	}
	m.extensions.err = ""
	updated, cmd = m.handleExtensionsKey(tea.KeyPressMsg(tea.Key{Code: 'd', Text: "d"}))
	m = updated.(*model)
	if cmd != nil || m.extensions.confirm != nil || m.extensions.err != "Plugin is not installed" {
		t.Fatalf("invalid uninstall state=%#v", m.extensions)
	}
	m.extensions.err = ""
	updated, cmd = m.handleExtensionsKey(tea.KeyPressMsg(tea.Key{Code: 'i', Text: "i"}))
	m = updated.(*model)
	if cmd == nil || cmd().(extensionsEvent).err != nil || strings.Join(actions, ",") != "update,uninstall,install" {
		t.Fatalf("install action=%v state=%#v", actions, m.extensions)
	}
}
