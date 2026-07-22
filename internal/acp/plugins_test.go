package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/plugin"
	sessionlog "github.com/lookcorner/go-cli/internal/session"
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
