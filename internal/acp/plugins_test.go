package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/plugin"
	sessionlog "github.com/lookcorner/go-cli/internal/session"
	"github.com/lookcorner/go-cli/internal/skills"
	"github.com/lookcorner/go-cli/internal/tools"
)

func TestPluginUpdatesNotifyKnownSession(t *testing.T) {
	logger, err := sessionlog.NewLoggerWithID(t.TempDir(), "plugin-session")
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()

	var output bytes.Buffer
	server := &Server{
		output: &output,
		sessions: map[string]*session{
			"plugin-session": {id: "plugin-session", runner: &agent.Runner{Logger: logger}},
		},
	}
	server.handlePlugins(context.Background(), message{
		ID:     json.RawMessage("1"),
		Method: "x.ai/plugins/notify-updates",
		Params: json.RawMessage(`{"sessionId":"plugin-session","updates":[["review","1.0.0","1.1.0"]]}`),
	})

	messages := decodeACPOutput(t, output.Bytes())
	if len(messages) != 2 || messages[0]["method"] != "x.ai/session_notification" {
		t.Fatalf("messages=%#v", messages)
	}
	params := messages[0]["params"].(map[string]any)
	update := params["update"].(map[string]any)
	updates := update["updates"].([]any)
	tuple := updates[0].([]any)
	if params["sessionId"] != "plugin-session" || update["sessionUpdate"] != "plugin_updates_installed" || len(tuple) != 3 || tuple[0] != "review" || tuple[1] != "1.0.0" || tuple[2] != "1.1.0" {
		t.Fatalf("notification=%#v", messages[0])
	}
	result := messages[1]["result"].(map[string]any)
	if result["ok"] != true {
		t.Fatalf("response=%#v", messages[1])
	}
	persisted, err := sessionlog.Events(logger.Path(), "xai_session_notification")
	if err != nil || len(persisted) != 1 {
		t.Fatalf("persisted=%#v err=%v", persisted, err)
	}
}

func TestPluginUpdatesUnknownSessionSucceedsThroughServe(t *testing.T) {
	var output bytes.Buffer
	server := &Server{Factory: func(context.Context, SessionConfig, tools.Approver, io.Writer, io.Writer) (*agent.Runner, func(), error) {
		t.Fatal("plugin update notification started a session")
		return nil, nil, nil
	}}
	input := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"x.ai/plugins/notify-updates","params":{"sessionId":"missing","updates":[]}}` + "\n")
	if err := server.Serve(context.Background(), input, &output); err != nil {
		t.Fatal(err)
	}
	messages := decodeACPOutput(t, output.Bytes())
	if len(messages) != 1 || messages[0]["result"].(map[string]any)["ok"] != true {
		t.Fatalf("messages=%#v", messages)
	}
}

func TestPluginUpdatesRejectMalformedParameters(t *testing.T) {
	for _, params := range []string{
		`{}`,
		`{"sessionId":"session"}`,
		`{"sessionId":"session","updates":null}`,
		`{"sessionId":"session","updates":[["plugin","1.0.0"]]}`,
		`{"sessionId":"session","updates":[["plugin","1.0.0","1.1.0","extra"]]}`,
		`{"sessionId":"session","updates":[["plugin",1,"1.1.0"]]}`,
	} {
		t.Run(params, func(t *testing.T) {
			var output bytes.Buffer
			server := &Server{output: &output, sessions: map[string]*session{}}
			server.handlePlugins(context.Background(), message{ID: json.RawMessage("1"), Method: "x.ai/plugins/notify-updates", Params: json.RawMessage(params)})
			messages := decodeACPOutput(t, output.Bytes())
			errorValue := messages[0]["error"].(map[string]any)
			if len(messages) != 1 || errorValue["code"] != float64(-32602) {
				t.Fatalf("messages=%#v", messages)
			}
		})
	}
}

func TestPluginsReloadRefreshesLocalInstallAndFansOutOnce(t *testing.T) {
	t.Setenv("GROK_HOME", filepath.Join(t.TempDir(), ".grok"))
	source := filepath.Join(t.TempDir(), "source")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(source, "plugin.json")
	if err := os.WriteFile(manifest, []byte(`{"name":"alpha","version":"1.0.0"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	installed, err := plugin.Install(source, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifest, []byte(`{"name":"alpha","version":"2.0.0"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	calls, version := 0, ""
	update := func(context.Context, func(*plugin.Settings)) ([]plugin.Plugin, error) {
		calls++
		registry, err := plugin.LoadInstallRegistry()
		if err == nil {
			version = registry.Repos[installed.RepoKey].Plugins["alpha"].Version
		}
		return nil, err
	}
	var output bytes.Buffer
	server := &Server{output: &output, sessions: map[string]*session{
		"one": {id: "one", running: true, runner: &agent.Runner{UpdatePlugins: update}},
		"two": {id: "two", running: true, runner: &agent.Runner{UpdatePlugins: update}},
	}}
	server.handlePlugins(context.Background(), message{ID: json.RawMessage("1"), Method: "x.ai/plugins/reload"})
	messages := decodeACPOutput(t, output.Bytes())
	if len(messages) != 1 || messages[0]["result"].(map[string]any)["ok"] != true || calls != 1 || version != "2.0.0" {
		t.Fatalf("messages=%#v calls=%d version=%q", messages, calls, version)
	}
}

func TestPluginsReloadRouteSucceedsWithoutSessionsOnRefreshFailure(t *testing.T) {
	grokHome := filepath.Join(t.TempDir(), ".grok")
	t.Setenv("GROK_HOME", grokHome)
	registryPath := filepath.Join(grokHome, "installed-plugins", "registry.json")
	if err := os.MkdirAll(filepath.Dir(registryPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(registryPath, []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	server := &Server{Factory: func(context.Context, SessionConfig, tools.Approver, io.Writer, io.Writer) (*agent.Runner, func(), error) {
		t.Fatal("plugin reload started a session")
		return nil, nil, nil
	}}
	input := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"x.ai/plugins/reload","params":{}}` + "\n")
	if err := server.Serve(context.Background(), input, &output); err != nil {
		t.Fatal(err)
	}
	messages := decodeACPOutput(t, output.Bytes())
	if len(messages) != 1 || messages[0]["result"].(map[string]any)["ok"] != true {
		t.Fatalf("messages=%#v", messages)
	}
}

func TestPluginSlashListFormatting(t *testing.T) {
	root := t.TempDir()
	skillDir := filepath.Join(root, "skills")
	agentDir := filepath.Join(root, "agents")
	if err := os.MkdirAll(filepath.Join(skillDir, "review"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(agentDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "worker.md"), []byte("---\nname: worker\n---\nWork."), 0o600); err != nil {
		t.Fatal(err)
	}
	text := pluginListText([]plugin.Plugin{{
		Name: "team-tools", Version: "1.2.3", Scope: "project", Root: root,
		Enabled: true, Trusted: false, SkillDirs: []string{skillDir}, AgentDirs: []string{agentDir},
		InlineHooks: json.RawMessage(`{"hooks":{}}`), InlineMCP: json.RawMessage(`{"mcpServers":{"docs":{}}}`),
	}})
	for _, want := range []string{
		"Installed plugins (1):", "team-tools v1.2.3 (project [untrusted])",
		"1 skills, 1 agents, hooks: active (inline), 1 MCP servers (inline)", "Run: /plugins trust " + root,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in %q", want, text)
		}
	}
	if got := pluginListText(nil); got != "No plugins installed." {
		t.Fatalf("empty list=%q", got)
	}
}

func TestPluginSlashCommandsManagePluginsWithoutModelTurn(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GROK_HOME", filepath.Join(t.TempDir(), ".grok"))
	source := filepath.Join(root, "source")
	for _, name := range []string{"alpha", "beta"} {
		pluginRoot := filepath.Join(source, name)
		if err := os.MkdirAll(filepath.Join(pluginRoot, "skills", name), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(pluginRoot, "plugin.json"), []byte(`{"name":"`+name+`","version":"1.0.0"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(pluginRoot, "skills", name, "SKILL.md"), []byte("---\nname: "+name+"\ndescription: "+name+"\n---\n"+name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	catalog, err := skills.Discover(root, skills.Config{})
	if err != nil {
		t.Fatal(err)
	}
	settings := plugin.Settings{}
	var inventory []plugin.Plugin
	updates := 0
	runner := &agent.Runner{Skills: catalog}
	runner.PluginInventory = func() []plugin.Plugin { return append([]plugin.Plugin(nil), inventory...) }
	runner.UpdatePlugins = func(_ context.Context, update func(*plugin.Settings)) ([]plugin.Plugin, error) {
		updates++
		if update != nil {
			update(&settings)
		}
		inventory, err = plugin.Inventory(root, plugin.Config{Paths: settings.Paths, Enabled: settings.Enabled, Disabled: settings.Disabled, ProjectTrusted: true})
		if err == nil {
			err = catalog.ReconfigurePlugins(enabledPluginFixtures(inventory))
		}
		return append([]plugin.Plugin(nil), inventory...), err
	}
	current := &session{id: "plugin-slash", cwd: root, runner: runner, activePrompt: -1}
	blocker := &session{id: "blocker", running: false}
	var output bytes.Buffer
	server := &Server{output: &output, sessions: map[string]*session{current.id: current, blocker.id: blocker}}
	request := func(id int, prompt string) string {
		t.Helper()
		output.Reset()
		params, _ := json.Marshal(map[string]any{"sessionId": current.id, "prompt": []any{map[string]any{"type": "text", "text": prompt}}})
		server.handlePrompt(context.Background(), message{ID: json.RawMessage(strconv.Itoa(id)), Method: "session/prompt", Params: params})
		messages := decodeACPOutput(t, output.Bytes())
		text := ""
		responded := false
		for _, item := range messages {
			params, _ := item["params"].(map[string]any)
			update, _ := params["update"].(map[string]any)
			content, _ := update["content"].(map[string]any)
			if value, _ := content["text"].(string); value != "" {
				text = value
			}
			result, _ := item["result"].(map[string]any)
			responded = responded || result["stopReason"] == "end_turn"
		}
		if !responded {
			t.Fatalf("prompt=%q messages=%#v", prompt, messages)
		}
		return text
	}

	for id, prompt := range []string{"/plugins", "/plugin", "/plugins list", "/plugins unknown"} {
		if got := request(id+1, prompt); got != "No plugins installed." {
			t.Fatalf("prompt=%q text=%q", prompt, got)
		}
	}
	if got := request(5, "/plugins trust "+root); !strings.Contains(got, "replaced by enable/disable") {
		t.Fatalf("trust=%q", got)
	}
	if got := request(6, "/plugins install "+source); !strings.Contains(got, "To proceed, re-run with --trust") || len(inventory) != 0 {
		t.Fatalf("preview=%q inventory=%#v", got, inventory)
	}
	if got := request(7, "/plugins install "+filepath.Join(root, "missing")+" --trust"); !strings.HasPrefix(got, "Failed to install plugin:") || len(inventory) != 0 {
		t.Fatalf("failed install=%q inventory=%#v", got, inventory)
	}
	blocker.running = true
	if got := request(8, "/plugins add "+filepath.Join(source, "alpha")); !strings.Contains(got, "prompt is running") || len(settings.Paths) != 0 {
		t.Fatalf("running guard=%q settings=%#v", got, settings)
	}
	blocker.running = false
	alphaPath := filepath.Join(source, "alpha")
	resolvedAlpha := plugin.ResolvePath(alphaPath, root)
	if got := request(9, "/plugins add "+alphaPath); got != "Added plugin path: "+resolvedAlpha || len(inventory) != 1 {
		t.Fatalf("add=%q inventory=%#v", got, inventory)
	}
	if got := request(10, "/plugins remove "+alphaPath); got != "Removed plugin path: "+resolvedAlpha || len(inventory) != 0 {
		t.Fatalf("remove=%q inventory=%#v", got, inventory)
	}
	if got := request(11, "/plugins install "+source+" --trust"); !strings.Contains(got, "Installed 2 plugin(s)") || len(inventory) != 2 || strings.Join(catalog.Names(), "|") != "alpha:alpha|beta:beta" {
		t.Fatalf("install=%q inventory=%#v skills=%v", got, inventory, catalog.Names())
	}
	if got := request(12, "/plugins list"); !strings.Contains(got, "Installed plugins (2):") || !strings.Contains(got, "alpha v1.0.0") {
		t.Fatalf("list=%q", got)
	}
	if got := request(13, "/plugins update"); !strings.Contains(got, "updated") {
		t.Fatalf("update all=%q", got)
	}
	if err := os.WriteFile(filepath.Join(source, "alpha", "plugin.json"), []byte(`{"name":"alpha","version":"2.0.0"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := request(14, "/plugins update alpha"); !strings.Contains(got, "updated") {
		t.Fatalf("update=%q", got)
	}
	if got := request(15, "/reload-plugins"); got != "Plugins reloaded." {
		t.Fatalf("reload alias=%q", got)
	}
	if got := request(16, "/plugins reload"); got != "Plugins reloaded." {
		t.Fatalf("reload=%q", got)
	}
	if got := request(17, "/plugins uninstall alpha"); !strings.Contains(got, "--confirm") || len(inventory) != 2 {
		t.Fatalf("confirmation=%q inventory=%#v", got, inventory)
	}
	if got := request(18, "/plugins uninstall alpha --confirm"); !strings.HasPrefix(got, "Uninstalled repo ") || len(inventory) != 0 || len(catalog.Names()) != 0 {
		t.Fatalf("uninstall=%q inventory=%#v skills=%v", got, inventory, catalog.Names())
	}
	if current.promptIndex != 0 || updates < 6 {
		t.Fatalf("promptIndex=%d updates=%d", current.promptIndex, updates)
	}
}

func TestPluginSlashMutationRequiresWritableCapability(t *testing.T) {
	current := &session{id: "plugin-read-only", runner: &agent.Runner{PluginInventory: func() []plugin.Plugin { return nil }}}
	server := &Server{sessions: map[string]*session{current.id: current}}
	if got := server.pluginSlashText(context.Background(), current, pluginCommand{action: "reload"}); got != "Plugin configuration is read-only." {
		t.Fatalf("reload=%q", got)
	}
}
