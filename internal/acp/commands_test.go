package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/api"
	"github.com/lookcorner/go-cli/internal/hooks"
	mcppkg "github.com/lookcorner/go-cli/internal/mcp"
	"github.com/lookcorner/go-cli/internal/plugin"
	sessionlog "github.com/lookcorner/go-cli/internal/session"
	"github.com/lookcorner/go-cli/internal/skills"
	"github.com/lookcorner/go-cli/internal/tools"
	"github.com/lookcorner/go-cli/internal/workspace"
)

func TestCommandsListAdvertisesCapabilitiesAndSkills(t *testing.T) {
	root := t.TempDir()
	userRoot := t.TempDir()
	writeCommandSkill(t, root, "deploy", "---\nname: deploy\ndescription: Deploy the service\nuser-invocable: true\nargument-hint: environment\nmetadata:\n  short-description: Safe deploy\n---\nDeploy it.\n")
	writeCommandSkill(t, root, "compact", "---\nname: compact\ndescription: Skill compact\nuser-invocable: true\n---\nCompact it.\n")
	writeCommandSkill(t, root, "plugins", "---\nname: plugins\ndescription: Skill plugins\nuser-invocable: true\n---\nPlugin skill.\n")
	writeCommandSkill(t, root, "feedback", "---\nname: feedback\ndescription: Skill feedback\nuser-invocable: true\n---\nFeedback skill.\n")
	writeCommandSkill(t, root, "hidden", "---\nname: hidden\ndescription: Hidden command\nuser-invocable: false\n---\nHidden.\n")
	writeCommandSkill(t, userRoot, "global", "---\nname: global\ndescription: Global command\nuser-invocable: true\n---\nGlobal.\n")
	catalog, err := skills.Discover(root, skills.Config{Paths: []string{filepath.Join(userRoot, ".grok", "skills")}})
	if err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry(ws, tools.PromptApprover{Mode: tools.PermissionAuto})
	defer registry.Close()
	runner := &agent.Runner{Tools: registry, Skills: catalog, HookCatalog: hooks.DiscoverPlugins(nil), PluginInventory: func() []plugin.Plugin { return nil }, MCPServerCatalog: func() []mcppkg.ServerConfig { return nil }, SubmitFeedback: func(sessionlog.UserFeedback) error { return nil }, SharingEnabled: func() bool { return true }}
	var output bytes.Buffer
	server := &Server{output: &output, sessions: map[string]*session{"commands": {id: "commands", cwd: root, runner: runner}}}
	server.handleCommands(message{ID: json.RawMessage("1"), Params: json.RawMessage(`{"cwd":` + quoted(root) + `}`)})
	messages := decodeACPOutput(t, output.Bytes())
	commands := messages[0]["result"].(map[string]any)["commands"].([]any)
	byName := make(map[string]map[string]any, len(commands))
	for _, raw := range commands {
		command := raw.(map[string]any)
		byName[command["name"].(string)] = command
	}
	for _, name := range []string{"compact", "always-approve", "privacy", "terminal-setup", "usage", "release-notes", "share", "mcps", "context", "session-info", "hooks-trust", "hooks-list", "hooks-add", "hooks-remove", "hooks-untrust", "plugins", "reload-plugins", "feedback", "goal", "loop", "local:compact", "local:plugins", "local:feedback", "deploy"} {
		if byName[name] == nil {
			t.Fatalf("missing command %q in %#v", name, commands)
		}
	}
	if byName["hidden"] != nil || byName["compact"]["_meta"] != nil {
		t.Fatalf("invalid command filtering/collision: %#v", commands)
	}
	if byName["compact"]["input"].(map[string]any)["hint"] != "optional context about what to preserve" || byName["loop"]["input"].(map[string]any)["hint"] != "[interval] <prompt>" {
		t.Fatalf("command input hints=%#v", commands)
	}
	deploy := byName["deploy"]
	if deploy["description"] != "Safe deploy" || deploy["input"].(map[string]any)["hint"] != "environment" {
		t.Fatalf("deploy command=%#v", deploy)
	}
	meta := deploy["_meta"].(map[string]any)
	if meta["scope"] != "local" || meta["path"] == "" {
		t.Fatalf("deploy meta=%#v", meta)
	}

	output.Reset()
	server.handleCommands(message{ID: json.RawMessage("2"), Params: json.RawMessage(`{}`)})
	messages = decodeACPOutput(t, output.Bytes())
	commands = messages[0]["result"].(map[string]any)["commands"].([]any)
	byName = make(map[string]map[string]any, len(commands))
	for _, raw := range commands {
		command := raw.(map[string]any)
		byName[command["name"].(string)] = command
	}
	if byName["global"] == nil || byName["deploy"] != nil || byName["local:compact"] != nil {
		t.Fatalf("global commands=%#v", commands)
	}
}

func TestBuiltinCommandsFollowReferenceOrderAndCapabilityGates(t *testing.T) {
	runner := &agent.Runner{HookCatalog: hooks.DiscoverPlugins(nil), PluginInventory: func() []plugin.Plugin { return nil }, MCPServerCatalog: func() []mcppkg.ServerConfig { return nil }, SubmitFeedback: func(sessionlog.UserFeedback) error { return nil }}
	commands := availableCommands(runner, true)
	names := make([]string, 0, len(commands))
	for _, command := range commands {
		names = append(names, command["name"].(string))
	}
	want := []string{"compact", "always-approve", "privacy", "terminal-setup", "usage", "release-notes", "mcps", "context", "hooks-trust", "hooks-list", "hooks-add", "hooks-remove", "hooks-untrust", "plugins", "reload-plugins", "session-info", "feedback"}
	if strings.Join(names, "|") != strings.Join(want, "|") {
		t.Fatalf("commands=%v want=%v", names, want)
	}
	for _, command := range availableCommands(&agent.Runner{}, true) {
		if name := command["name"].(string); name == "plugins" || name == "reload-plugins" {
			t.Fatalf("plugin command advertised without capability: %#v", command)
		}
	}
	for _, command := range availableCommands(&agent.Runner{SharingEnabled: func() bool { return false }}, true) {
		if command["name"] == "share" {
			t.Fatalf("share command advertised while disabled: %#v", command)
		}
	}
}

func TestFeedbackCommandRequiresCapabilityAndExactPrefix(t *testing.T) {
	for _, command := range availableCommands(&agent.Runner{}, true) {
		if command["name"] == "feedback" {
			t.Fatalf("feedback advertised without capability: %#v", command)
		}
	}
	for _, test := range []struct {
		prompt, text string
		ok           bool
	}{
		{"/feedback", "", true},
		{" /feedback useful command ", "useful command", true},
		{"/feedbacks no", "", false},
		{"/feedback-now", "", false},
	} {
		text, ok := parseFeedbackCommand(test.prompt)
		if text != test.text || ok != test.ok {
			t.Errorf("prompt=%q text=%q ok=%v", test.prompt, text, ok)
		}
	}
}

func TestParsePluginCommand(t *testing.T) {
	tests := []struct {
		prompt, action, value string
		confirm               bool
		ok                    bool
	}{
		{"/plugins", "list", "", false, true},
		{" /plugin list ", "list", "", false, true},
		{"/plugins reload", "reload", "", false, true},
		{"/reload-plugins", "reload", "", false, true},
		{"/plugins trust-anything", "trust", "", false, true},
		{"/plugins add ./local plugin", "add", "./local plugin", false, true},
		{"/plugins remove ./local", "remove", "./local", false, true},
		{"/plugins install owner/repo", "install", "owner/repo", false, true},
		{"/plugins install owner/repo --trust", "install", "owner/repo", true, true},
		{"/plugins install owner/repo --trust extra", "install", "owner/repo --trust extra", false, true},
		{"/plugins uninstall alpha --confirm", "uninstall", "alpha", true, true},
		{"/plugins update", "update", "", false, true},
		{"/plugins update alpha", "update", "alpha", false, true},
		{"/plugins unknown", "list", "", false, true},
		{"/plugins-list", "", "", false, false},
		{"/reload-plugins now", "", "", false, false},
	}
	for _, test := range tests {
		command, ok := parsePluginCommand(test.prompt)
		if command.action != test.action || command.value != test.value || command.confirm != test.confirm || ok != test.ok {
			t.Errorf("prompt=%q command=%#v ok=%v", test.prompt, command, ok)
		}
	}
}

func TestHookCommandsRequireCatalog(t *testing.T) {
	for _, command := range availableCommands(&agent.Runner{}, true) {
		if strings.HasPrefix(command["name"].(string), "hooks-") {
			t.Fatalf("hook command advertised without a catalog: %#v", command)
		}
	}
	for _, test := range []struct {
		prompt, action, path string
		ok                   bool
	}{
		{"/hooks-list", "list", "", true},
		{" /hooks-add  /tmp/my hooks.json ", "add", "/tmp/my hooks.json", true},
		{"/hooks-remove", "remove", "", true},
		{"/hooks-listing", "", "", false},
	} {
		action, path, ok := parseHookCommand(test.prompt)
		if action != test.action || path != test.path || ok != test.ok {
			t.Errorf("prompt=%q action=%q path=%q ok=%v", test.prompt, action, path, ok)
		}
	}
}

func TestParseGoalCommandConsumesOnlyValidTrailingBudget(t *testing.T) {
	tests := []struct {
		prompt, action, objective string
		budget                    int64
		ok                        bool
	}{
		{"/goal", "status", "", 0, true},
		{"/goal STATUS", "status", "", 0, true},
		{"/goal pause", "pause", "", 0, true},
		{"/goal ship it --budget 1200", "set", "ship it", 1200, true},
		{"/goal\u2003ship it --budget\u20031200", "set", "ship it", 1200, true},
		{"/goal mention --budget later", "set", "mention --budget later", 0, true},
		{"/goal ship --budget 0", "set", "ship --budget 0", 0, true},
		{"/goal ship --budget 12 extra", "set", "ship --budget 12 extra", 0, true},
		{"/goal ship--budget 12", "set", "ship--budget 12", 0, true},
		{"/goalkeeper ship", "", "", 0, false},
	}
	for _, test := range tests {
		got, ok := parseGoalCommand(test.prompt)
		if ok != test.ok || got.action != test.action || got.objective != test.objective || got.budget != test.budget {
			t.Errorf("prompt=%q got=%#v ok=%v", test.prompt, got, ok)
		}
	}
}

func TestCommandsListValidatesParamsAndInitializeAdvertisesPreSessionCommands(t *testing.T) {
	var output bytes.Buffer
	server := &Server{output: &output, sessions: map[string]*session{}}
	server.handleCommands(message{ID: json.RawMessage("1"), Params: json.RawMessage(`{`)})
	messages := decodeACPOutput(t, output.Bytes())
	if len(messages) != 1 || messages[0]["error"].(map[string]any)["message"] != "invalid commands list parameters" {
		t.Fatalf("messages=%#v", messages)
	}

	output.Reset()
	routed := &Server{Factory: func(context.Context, SessionConfig, tools.Approver, io.Writer, io.Writer) (*agent.Runner, func(), error) {
		return nil, nil, nil
	}}
	input := strings.NewReader(
		`{"jsonrpc":"2.0","id":2,"method":"initialize","params":{"protocolVersion":1}}` + "\n" +
			`{"jsonrpc":"2.0","id":3,"method":"x.ai/commands/list","params":{}}`,
	)
	if err := routed.Serve(context.Background(), input, &output); err != nil {
		t.Fatal(err)
	}
	messages = decodeACPOutput(t, output.Bytes())
	meta := messages[0]["result"].(map[string]any)["_meta"].(map[string]any)
	commands := meta["availableCommands"].([]any)
	if len(commands) != 8 || commands[0].(map[string]any)["name"] != "compact" || commands[0].(map[string]any)["input"].(map[string]any)["hint"] == "" || commands[1].(map[string]any)["name"] != "always-approve" || commands[1].(map[string]any)["input"].(map[string]any)["hint"] != "on|off" || commands[2].(map[string]any)["name"] != "privacy" || commands[2].(map[string]any)["input"].(map[string]any)["hint"] != "opt-out" || commands[3].(map[string]any)["name"] != "terminal-setup" || commands[4].(map[string]any)["name"] != "usage" || commands[4].(map[string]any)["input"].(map[string]any)["hint"] != "show | manage" || commands[5].(map[string]any)["name"] != "release-notes" || commands[6].(map[string]any)["name"] != "context" || commands[7].(map[string]any)["name"] != "session-info" {
		t.Fatalf("available commands=%#v", commands)
	}
	routedCommands := messages[1]["result"].(map[string]any)["commands"].([]any)
	if len(routedCommands) != 8 || routedCommands[7].(map[string]any)["name"] != "session-info" {
		t.Fatalf("routed commands=%#v", routedCommands)
	}
}

func TestPrivacySlashCommandCompletesLocally(t *testing.T) {
	streamer := &fixtureStreamer{}
	current := &session{id: "privacy-command", runner: &agent.Runner{Client: streamer, Model: "test"}, activePrompt: -1}
	var output bytes.Buffer
	server := &Server{output: &output, sessions: map[string]*session{current.id: current}}

	tests := []struct {
		prompt, want string
	}{
		{"/privacy", "Product: Gork Build"},
		{"/privacy private", "Coding data sharing: Opt out"},
		{"/privacy OPT-IN", agent.PrivacyLockedMessage},
		{"/privacy on", "Unknown argument"},
	}
	for id, test := range tests {
		output.Reset()
		params, _ := json.Marshal(map[string]any{"sessionId": current.id, "prompt": []any{map[string]any{"type": "text", "text": test.prompt}}})
		server.handlePrompt(context.Background(), message{ID: json.RawMessage(fmt.Sprintf("%d", id+20)), Method: "session/prompt", Params: params})
		messages := decodeACPOutput(t, output.Bytes())
		textFound, completed := false, false
		for _, item := range messages {
			result, ok := item["result"].(map[string]any)
			completed = completed || ok && result["stopReason"] == "end_turn"
			params, _ := item["params"].(map[string]any)
			update, _ := params["update"].(map[string]any)
			content, _ := update["content"].(map[string]any)
			text, _ := content["text"].(string)
			textFound = textFound || strings.Contains(text, test.want)
		}
		if !textFound || !completed || len(streamer.requests) != 0 || current.promptIndex != 0 {
			t.Fatalf("prompt=%q text=%v completed=%v requests=%d promptIndex=%d messages=%#v", test.prompt, textFound, completed, len(streamer.requests), current.promptIndex, messages)
		}
	}
}

func TestMCPSlashCommandCompletesLocally(t *testing.T) {
	root := t.TempDir()
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry(ws, nil)
	defer registry.Close()
	streamer := &fixtureStreamer{}
	current := &session{id: "mcps-command", runner: &agent.Runner{
		Client: streamer, Model: "test", Tools: registry,
		MCPServerCatalog: func() []mcppkg.ServerConfig {
			return []mcppkg.ServerConfig{{Name: "remote`name\x1b", URL: "https://mcp.example/v1"}, {Name: "local", Command: "npx", Args: []string{"server"}, Disabled: true}}
		},
	}, activePrompt: -1}
	var output bytes.Buffer
	server := &Server{output: &output, sessions: map[string]*session{current.id: current}}
	params, _ := json.Marshal(map[string]any{"sessionId": current.id, "prompt": []any{map[string]any{"type": "text", "text": "/mcps"}}})
	server.handlePrompt(context.Background(), message{ID: json.RawMessage("28"), Method: "session/prompt", Params: params})
	messages := decodeACPOutput(t, output.Bytes())
	encoded, _ := json.Marshal(messages)
	text := string(encoded)
	if !strings.Contains(text, "MCP servers") || !strings.Contains(text, "enabled") || !strings.Contains(text, "disabled") || strings.Contains(text, "\\u001b") || len(streamer.requests) != 0 || current.promptIndex != 0 {
		t.Fatalf("requests=%d promptIndex=%d messages=%#v", len(streamer.requests), current.promptIndex, messages)
	}
}

func TestTerminalSetupSlashCommandCompletesLocally(t *testing.T) {
	streamer := &fixtureStreamer{}
	current := &session{id: "terminal-setup-command", runner: &agent.Runner{Client: streamer, Model: "test"}, activePrompt: -1}
	var output bytes.Buffer
	server := &Server{output: &output, sessions: map[string]*session{current.id: current}}
	for id, prompt := range []string{"/terminal-setup", "/terminal-check ignored", "/terminal-info"} {
		output.Reset()
		params, _ := json.Marshal(map[string]any{"sessionId": current.id, "prompt": []any{map[string]any{"type": "text", "text": prompt}}})
		server.handlePrompt(context.Background(), message{ID: json.RawMessage(fmt.Sprintf("%d", id+30)), Method: "session/prompt", Params: params})
		messages := decodeACPOutput(t, output.Bytes())
		textFound, completed := false, false
		for _, item := range messages {
			result, ok := item["result"].(map[string]any)
			completed = completed || ok && result["stopReason"] == "end_turn"
			params, _ := item["params"].(map[string]any)
			update, _ := params["update"].(map[string]any)
			content, _ := update["content"].(map[string]any)
			text, _ := content["text"].(string)
			textFound = textFound || strings.Contains(text, "Environment\n") && strings.Contains(text, "Clipboard routes")
		}
		if !textFound || !completed || len(streamer.requests) != 0 || current.promptIndex != 0 {
			t.Fatalf("prompt=%q text=%v completed=%v requests=%d promptIndex=%d messages=%#v", prompt, textFound, completed, len(streamer.requests), current.promptIndex, messages)
		}
	}
}

func TestUsageSlashCommandCompletesLocally(t *testing.T) {
	streamer := &fixtureStreamer{}
	fetches, opened := 0, ""
	current := &session{id: "usage-command", runner: &agent.Runner{
		Client: streamer, Model: "test",
		FetchUsage: func(context.Context) (string, error) {
			fetches++
			if fetches == 2 {
				return "", errors.New("offline")
			}
			return "Weekly limit: 42%", nil
		},
		OpenURL: func(url string) bool { opened = url; return false },
	}, activePrompt: -1}
	var output bytes.Buffer
	server := &Server{output: &output, sessions: map[string]*session{current.id: current}}
	tests := []struct{ prompt, want string }{
		{"/usage", "Weekly limit: 42%"},
		{"/cost show", "Usage could not be loaded: offline"},
		{"/usage manage", "https://grok.com/?_s=usage"},
		{"/usage BAD", "Unknown argument: BAD"},
	}
	for id, test := range tests {
		output.Reset()
		params, _ := json.Marshal(map[string]any{"sessionId": current.id, "prompt": []any{map[string]any{"type": "text", "text": test.prompt}}})
		server.handlePrompt(context.Background(), message{ID: json.RawMessage(fmt.Sprintf("%d", id+40)), Method: "session/prompt", Params: params})
		messages := decodeACPOutput(t, output.Bytes())
		textFound, completed := false, false
		for _, item := range messages {
			result, ok := item["result"].(map[string]any)
			completed = completed || ok && result["stopReason"] == "end_turn"
			params, _ := item["params"].(map[string]any)
			update, _ := params["update"].(map[string]any)
			content, _ := update["content"].(map[string]any)
			text, _ := content["text"].(string)
			textFound = textFound || strings.Contains(text, test.want)
		}
		if !textFound || !completed || len(streamer.requests) != 0 || current.promptIndex != 0 {
			t.Fatalf("prompt=%q text=%v completed=%v requests=%d promptIndex=%d messages=%#v", test.prompt, textFound, completed, len(streamer.requests), current.promptIndex, messages)
		}
	}
	if fetches != 2 || opened != "https://grok.com/?_s=usage" {
		t.Fatalf("fetches=%d opened=%q", fetches, opened)
	}
}

func TestShareSlashCommandCompletesLocally(t *testing.T) {
	streamer := &fixtureStreamer{}
	calls := 0
	current := &session{id: "share-command", runner: &agent.Runner{
		Client: streamer, Model: "test", SessionID: "share-command",
		SharingEnabled: func() bool { return true },
		ShareSession: func(context.Context) (string, error) {
			calls++
			if calls == 2 {
				return "", errors.New("offline")
			}
			return "https://web.example/build/share/one", nil
		},
	}, activePrompt: -1}
	var output bytes.Buffer
	server := &Server{output: &output, sessions: map[string]*session{current.id: current}}
	for id, test := range []struct{ prompt, want string }{
		{"/share", "Session shared: https://web.example/build/share/one"},
		{"/share ignored", "Couldn't share session: offline"},
	} {
		output.Reset()
		params, _ := json.Marshal(map[string]any{"sessionId": current.id, "prompt": []any{map[string]any{"type": "text", "text": test.prompt}}})
		server.handlePrompt(context.Background(), message{ID: json.RawMessage(fmt.Sprintf("%d", id+60)), Method: "session/prompt", Params: params})
		messages := decodeACPOutput(t, output.Bytes())
		encoded, _ := json.Marshal(messages)
		completed := false
		for _, item := range messages {
			result, ok := item["result"].(map[string]any)
			completed = completed || ok && result["stopReason"] == "end_turn"
		}
		if !bytes.Contains(encoded, []byte(test.want)) || !completed || len(streamer.requests) != 0 || current.promptIndex != 0 {
			t.Fatalf("prompt=%q calls=%d requests=%d messages=%#v", test.prompt, calls, len(streamer.requests), messages)
		}
	}

	current.runner.SharingEnabled = func() bool { return false }
	output.Reset()
	params, _ := json.Marshal(map[string]any{"sessionId": current.id, "prompt": []any{map[string]any{"type": "text", "text": "/share"}}})
	server.handlePrompt(context.Background(), message{ID: json.RawMessage("62"), Method: "session/prompt", Params: params})
	messages := decodeACPOutput(t, output.Bytes())
	completed := false
	for _, item := range messages {
		result, ok := item["result"].(map[string]any)
		completed = completed || ok && result["stopReason"] == "end_turn"
	}
	if encoded, _ := json.Marshal(messages); !bytes.Contains(encoded, []byte("Sharing is disabled")) || !completed || calls != 2 {
		t.Fatalf("calls=%d output=%s", calls, encoded)
	}
}

func TestReleaseNotesSlashCommandCompletesLocally(t *testing.T) {
	streamer := &fixtureStreamer{}
	requests := 0
	current := &session{id: "release-notes", runner: &agent.Runner{
		Client: streamer, Model: "test", FetchReleaseNotes: func(context.Context) (string, error) {
			requests++
			if requests == 2 {
				return "", errors.New("No release notes available (offline).")
			}
			return "# Release Notes\n\n- Added sessions", nil
		},
	}, activePrompt: -1}
	var output bytes.Buffer
	server := &Server{output: &output, sessions: map[string]*session{current.id: current}}
	for id, test := range []struct{ prompt, want string }{
		{"/release-notes ignored", "Added sessions"},
		{"/changelog", "No release notes available (offline)."},
	} {
		output.Reset()
		params, _ := json.Marshal(map[string]any{"sessionId": current.id, "prompt": []any{map[string]any{"type": "text", "text": test.prompt}}})
		server.handlePrompt(context.Background(), message{ID: json.RawMessage(fmt.Sprintf("%d", id+70)), Method: "session/prompt", Params: params})
		messages := decodeACPOutput(t, output.Bytes())
		encoded, _ := json.Marshal(messages)
		completed := false
		for _, item := range messages {
			result, ok := item["result"].(map[string]any)
			completed = completed || ok && result["stopReason"] == "end_turn"
		}
		if !bytes.Contains(encoded, []byte(test.want)) || !completed || len(streamer.requests) != 0 || current.promptIndex != 0 {
			t.Fatalf("prompt=%q requests=%d messages=%#v", test.prompt, requests, messages)
		}
	}
}

func TestAlwaysApproveSlashCommandUsesReferenceArgumentsWithoutModelTurn(t *testing.T) {
	registry := permissionRegistry(t, tools.PermissionPrompt)
	defer registry.Close()
	current := &session{id: "permission-command", runner: &agent.Runner{Tools: registry}, activePrompt: -1}
	var output bytes.Buffer
	server := &Server{output: &output, sessions: map[string]*session{current.id: current}}
	request := func(id int, prompt string) []map[string]any {
		t.Helper()
		output.Reset()
		params, _ := json.Marshal(map[string]any{
			"sessionId": current.id,
			"prompt":    []any{map[string]any{"type": "text", "text": prompt}},
		})
		server.handlePrompt(context.Background(), message{ID: json.RawMessage(fmt.Sprintf("%d", id)), Method: "session/prompt", Params: params})
		return decodeACPOutput(t, output.Bytes())
	}
	assertMode := func(id int, prompt string, want tools.PermissionMode) {
		t.Helper()
		messages := request(id, prompt)
		mode, ok := registry.PermissionMode()
		responded := false
		for _, item := range messages {
			result, isResult := item["result"].(map[string]any)
			responded = responded || isResult && result["stopReason"] == "end_turn"
			params, _ := item["params"].(map[string]any)
			update, _ := params["update"].(map[string]any)
			if update["sessionUpdate"] == "agent_message_chunk" {
				t.Fatalf("prompt=%q emitted model text: %#v", prompt, messages)
			}
		}
		if !ok || mode != want || !responded || current.promptIndex != 0 {
			t.Fatalf("prompt=%q mode=%q ok=%v responded=%v promptIndex=%d messages=%#v", prompt, mode, ok, responded, current.promptIndex, messages)
		}
	}

	assertMode(1, "/always-approve", tools.PermissionAlwaysApprove)
	for id, prompt := range []string{"/yolo off", "/always-approve false", "/always-approve 0", "/always-approve no", "/always-approve disable"} {
		assertMode(id+2, prompt, tools.PermissionPrompt)
	}
	assertMode(7, "/yolo OFF", tools.PermissionPrompt)
	assertMode(8, "/always-approve false extra", tools.PermissionAlwaysApprove)
	if err := registry.SetPermissionMode(tools.PermissionAuto); err != nil {
		t.Fatal(err)
	}
	assertMode(9, "/yolo off", tools.PermissionAuto)
	assertMode(10, "/always-approve", tools.PermissionAlwaysApprove)
	assertMode(11, "/always-approve off", tools.PermissionAuto)

	for name, test := range map[string]struct {
		registry *tools.Registry
		prompt   string
		want     tools.PermissionMode
	}{
		"deny":    {permissionRegistry(t, tools.PermissionDeny), "/always-approve", tools.PermissionDeny},
		"managed": {permissionRegistryWithAutoLock(t, tools.PermissionPrompt, true), "/always-approve", tools.PermissionPrompt},
		"initial": {permissionRegistry(t, tools.PermissionAlwaysApprove), "/yolo off", tools.PermissionPrompt},
	} {
		t.Run(name, func(t *testing.T) {
			defer test.registry.Close()
			current := &session{id: name, runner: &agent.Runner{Tools: test.registry}, activePrompt: -1}
			var output bytes.Buffer
			server := &Server{output: &output, sessions: map[string]*session{name: current}}
			params, _ := json.Marshal(map[string]any{"sessionId": name, "prompt": []any{map[string]any{"type": "text", "text": test.prompt}}})
			server.handlePrompt(context.Background(), message{ID: json.RawMessage("12"), Method: "session/prompt", Params: params})
			mode, ok := test.registry.PermissionMode()
			messages := decodeACPOutput(t, output.Bytes())
			responded := false
			for _, item := range messages {
				result, isResult := item["result"].(map[string]any)
				responded = responded || isResult && result["stopReason"] == "end_turn"
			}
			if !ok || mode != test.want || !responded {
				t.Fatalf("mode=%q ok=%v responded=%v messages=%#v", mode, ok, responded, messages)
			}
		})
	}
}

type forwardingGoalObserver struct {
	server    *Server
	sessionID string
}

func (o forwardingGoalObserver) GoalEvent(event tools.GoalEvent) {
	o.server.NotifyGoalEvent(o.sessionID, event)
}

func TestGoalSlashCommandLifecycleAndInference(t *testing.T) {
	root := t.TempDir()
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry(ws, tools.PromptApprover{Mode: tools.PermissionAuto})
	defer registry.Close()
	if err := registry.ConfigureGoalVerification(filepath.Join(t.TempDir(), "artifacts")); err != nil {
		t.Fatal(err)
	}
	streamer := &fixtureStreamer{results: []api.StreamResult{
		{ResponseID: "goal-set", Text: "working"},
		{ResponseID: "goal-resume", Text: "continuing"},
	}}
	current := &session{id: "goal-command", cwd: root, runner: &agent.Runner{Client: streamer, Tools: registry, Model: "test"}, activePrompt: -1}
	var output bytes.Buffer
	server := &Server{output: &output, sessions: map[string]*session{current.id: current}}
	registry.SetGoalObserver(forwardingGoalObserver{server: server, sessionID: current.id})
	request := func(id int, prompt string) []map[string]any {
		t.Helper()
		output.Reset()
		params, _ := json.Marshal(map[string]any{"sessionId": current.id, "prompt": []any{map[string]any{"type": "text", "text": prompt}}})
		server.handlePrompt(context.Background(), message{ID: json.RawMessage(fmt.Sprintf("%d", id)), Method: "session/prompt", Params: params})
		server.wg.Wait()
		return decodeACPOutput(t, output.Bytes())
	}
	findText := func(messages []map[string]any, want string) bool {
		for _, item := range messages {
			params, _ := item["params"].(map[string]any)
			update, _ := params["update"].(map[string]any)
			content, _ := update["content"].(map[string]any)
			if content["text"] == want {
				return true
			}
		}
		return false
	}
	findTextContaining := func(messages []map[string]any, parts ...string) bool {
		for _, item := range messages {
			params, _ := item["params"].(map[string]any)
			update, _ := params["update"].(map[string]any)
			content, _ := update["content"].(map[string]any)
			text, _ := content["text"].(string)
			matched := true
			for _, part := range parts {
				matched = matched && strings.Contains(text, part)
			}
			if matched {
				return true
			}
		}
		return false
	}

	if messages := request(1, "/goal status"); !findText(messages, "No goal is currently set. Use /goal <objective> to start one.") || len(streamer.requests) != 0 {
		t.Fatalf("empty status messages=%#v requests=%d", messages, len(streamer.requests))
	}
	request(2, "/goal ship the feature --budget 1200")
	snapshot := registry.GoalSnapshot()
	if snapshot.Objective != "ship the feature" || snapshot.Status != "active" || snapshot.TokenBudget != 1200 || len(streamer.requests) != 1 || !strings.Contains(fmt.Sprint(streamer.requests[0].Input), "ship the feature") {
		t.Fatalf("set snapshot=%#v requests=%#v", snapshot, streamer.requests)
	}
	if messages := request(3, "/goal replace it"); len(streamer.requests) != 1 || messages[len(messages)-1]["error"].(map[string]any)["message"] != "a goal is already active" {
		t.Fatalf("active replacement messages=%#v requests=%d", messages, len(streamer.requests))
	}
	status := request(4, "/goal")
	if !findTextContaining(status, "Goal: ship the feature\nStatus: Active | Phase: Executing\nTokens used: 0\nElapsed: ", " | Budget: 1200") || len(streamer.requests) != 1 {
		t.Fatalf("status messages=%#v requests=%d", status, len(streamer.requests))
	}
	if messages := request(5, "/goal pause"); !findText(messages, "Goal paused. Use /goal resume to continue.") || registry.GoalSnapshot().Status != "user_paused" || len(streamer.requests) != 1 {
		t.Fatalf("pause messages=%#v snapshot=%#v", messages, registry.GoalSnapshot())
	}
	if messages := request(6, "/goal pause"); !findText(messages, "Goal is already paused.") || len(streamer.requests) != 1 {
		t.Fatalf("second pause messages=%#v", messages)
	}
	if messages := request(7, "/goal resume"); !findText(messages, "Goal resumed.") || registry.GoalSnapshot().Status != "active" || len(streamer.requests) != 2 || !strings.Contains(fmt.Sprint(streamer.requests[1].Input), "Continue working toward the active goal") {
		t.Fatalf("resume messages=%#v snapshot=%#v requests=%#v", messages, registry.GoalSnapshot(), streamer.requests)
	}
	messages := request(8, "/goal clear")
	cleared := false
	for _, item := range messages {
		params, _ := item["params"].(map[string]any)
		update, _ := params["update"].(map[string]any)
		cleared = cleared || update["sessionUpdate"] == "goal_updated" && update["status"] == "cleared"
	}
	if !findText(messages, "Goal cleared.") || !cleared || registry.GoalSnapshot().Objective != "" || len(streamer.requests) != 2 {
		t.Fatalf("clear messages=%#v snapshot=%#v", messages, registry.GoalSnapshot())
	}
}

func TestHookSlashCommandsManageHooksWithoutModelTurn(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	t.Setenv("GROK_HOME", home)
	runACPGit(t, root, "init", "-q")
	gitRoot, ok := workspace.FindGitRoot(root)
	if !ok {
		t.Fatal("Git root was not detected")
	}
	hookFile := filepath.Join(root, "hooks.json")
	if err := os.WriteFile(hookFile, []byte(`{"hooks":{"PreToolUse":[{"matcher":"shell","hooks":[{"type":"command","command":"check","timeout":2}]}]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog := hooks.Discover(hooks.Config{ProjectTrusted: true, Plugins: []plugin.Plugin{{Name: "guard", Root: root, HooksConfig: hookFile, Executable: true}}})
	current := &session{id: "hook-command", cwd: root, runner: &agent.Runner{HookCatalog: catalog}, activePrompt: -1}
	var output bytes.Buffer
	server := &Server{output: &output, sessions: map[string]*session{current.id: current}}
	request := func(id int, prompt string) []map[string]any {
		t.Helper()
		output.Reset()
		params, _ := json.Marshal(map[string]any{"sessionId": current.id, "prompt": []any{map[string]any{"type": "text", "text": prompt}}})
		server.handlePrompt(context.Background(), message{ID: json.RawMessage(fmt.Sprintf("%d", id)), Method: "session/prompt", Params: params})
		return decodeACPOutput(t, output.Bytes())
	}
	text := func(messages []map[string]any) string {
		for _, item := range messages {
			params, _ := item["params"].(map[string]any)
			update, _ := params["update"].(map[string]any)
			content, _ := update["content"].(map[string]any)
			if value, _ := content["text"].(string); value != "" {
				return value
			}
		}
		return ""
	}

	listed := text(request(1, "/hooks-list"))
	if !strings.Contains(listed, "Loaded hooks (1):") || !strings.Contains(listed, "matcher: shell  command: check  timeout: 2s") {
		t.Fatalf("listed=%q", listed)
	}
	if got := text(request(2, "/hooks-add")); !strings.HasPrefix(got, "Usage: /hooks add <path>") {
		t.Fatalf("empty add=%q", got)
	}
	if got := text(request(3, "/hooks-add "+filepath.Join(t.TempDir(), "outside.json"))); !strings.HasPrefix(got, "Failed to add hook path:") {
		t.Fatalf("unsafe add=%q", got)
	}
	custom := filepath.Join(home, "custom", "hooks.json")
	if got := text(request(4, "/hooks-add "+custom)); got != "Added hook path: "+custom+"\nRestart session to load hooks from this path." {
		t.Fatalf("add=%q", got)
	}
	if got := text(request(5, "/hooks-remove "+custom)); got != "Removed hook path: "+custom+"\nRestart session to stop loading hooks from this path." {
		t.Fatalf("remove=%q", got)
	}
	if got := text(request(6, "/hooks-trust")); got != "Trusted: "+gitRoot+"." {
		t.Fatalf("trust=%q", got)
	}
	if got := text(request(7, "/hooks-untrust")); got != "Untrusted: "+gitRoot+"." {
		t.Fatalf("untrust=%q", got)
	}
	catalog.Reconfigure(hooks.Config{})
	if got := text(request(8, "/hooks-list")); got != "No hooks loaded for this session." {
		t.Fatalf("empty list=%q", got)
	}
	if got := text(request(9, "/hooks-untrust")); got != "Not currently trusted: "+gitRoot {
		t.Fatalf("second untrust=%q", got)
	}
	current.cwd = t.TempDir()
	if got := text(request(10, "/hooks-trust")); got != "Project hooks require a Git worktree." {
		t.Fatalf("non-Git trust=%q", got)
	}
	if current.promptIndex != 0 {
		t.Fatalf("hook commands started model turns: promptIndex=%d", current.promptIndex)
	}
}

func TestSessionStatusSlashCommandsUseLiveStateWithoutModelTurn(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry(ws, tools.PromptApprover{Mode: tools.PermissionAuto})
	defer registry.Close()
	runner := &agent.Runner{SessionID: "status-session", Workspace: "/work", ModelID: "grok-build", Model: "grok-build", ContextWindow: 1000, Tools: registry}
	current := &session{
		id: "status-session", cwd: "/work", title: "Session title", runner: runner,
		promptIndex: 2, activePrompt: -1, inputTokens: 250,
	}
	var output bytes.Buffer
	server := &Server{output: &output, sessions: map[string]*session{current.id: current}}
	request := func(id int, prompt string) []map[string]any {
		t.Helper()
		output.Reset()
		params, _ := json.Marshal(map[string]any{
			"sessionId": current.id,
			"prompt":    []any{map[string]any{"type": "text", "text": prompt}},
			"_meta":     map[string]any{"promptId": "status-prompt"},
		})
		server.handlePrompt(context.Background(), message{ID: json.RawMessage(fmt.Sprintf("%d", id)), Method: "session/prompt", Params: params})
		return decodeACPOutput(t, output.Bytes())
	}

	for id, prompt := range []string{"/session-info", "/status ignored", "/info extra"} {
		messages := request(id+1, prompt)
		text, completed, responded := "", false, false
		for _, item := range messages {
			params, _ := item["params"].(map[string]any)
			update, _ := params["update"].(map[string]any)
			content, _ := update["content"].(map[string]any)
			if update["sessionUpdate"] == "agent_message_chunk" {
				text, _ = content["text"].(string)
			}
			completed = completed || item["method"] == "x.ai/session/prompt_complete"
			if result, ok := item["result"].(map[string]any); ok && result["stopReason"] == "end_turn" {
				responded = true
			}
		}
		for _, want := range []string{"**Title:** Session title", "**Session ID:** status-session", "**Working directory:** /work", "**Model:** grok-build", "**Turn:** 2", "**Context:** 250 / 1000 tokens (25%)"} {
			if !strings.Contains(text, want) {
				t.Fatalf("prompt=%q missing %q in messages=%#v", prompt, want, messages)
			}
		}
		if !completed || !responded {
			t.Fatalf("prompt=%q messages=%#v", prompt, messages)
		}
	}

	messages := request(10, "/context ignored")
	for _, item := range messages {
		params, _ := item["params"].(map[string]any)
		update, _ := params["update"].(map[string]any)
		if update["sessionUpdate"] == "agent_message_chunk" {
			t.Fatalf("context command emitted text: %#v", messages)
		}
	}
	if result := messages[len(messages)-1]["result"].(map[string]any); result["stopReason"] != "end_turn" {
		t.Fatalf("context messages=%#v", messages)
	}

	output.Reset()
	server.handleSessionAdmin(message{ID: json.RawMessage("20"), Method: "x.ai/session/info", Params: json.RawMessage(`{"sessionId":"status-session"}`)})
	info := decodeACPOutput(t, output.Bytes())[0]["result"].(map[string]any)["result"].(map[string]any)
	contextInfo := info["context"].(map[string]any)
	if contextInfo["used"] != float64(250) || contextInfo["total"] != float64(1000) || contextInfo["usagePct"] != float64(25) || current.promptIndex != 2 {
		t.Fatalf("info=%#v promptIndex=%d", info, current.promptIndex)
	}

	current.runner.Client = &fixtureStreamer{results: []api.StreamResult{{ResponseID: "response-3", Text: "done", Usage: api.Usage{InputTokens: 333}}}}
	current.runner.ContextWindow = 0
	output.Reset()
	params, _ := json.Marshal(map[string]any{
		"sessionId": current.id,
		"prompt":    []any{map[string]any{"type": "text", "text": "regular model turn"}},
		"_meta":     map[string]any{"promptId": "regular-prompt"},
	})
	server.handlePrompt(context.Background(), message{ID: json.RawMessage("21"), Method: "session/prompt", Params: params})
	server.wg.Wait()
	modelMessages := decodeACPOutput(t, output.Bytes())
	modelResponded := false
	for _, item := range modelMessages {
		result, ok := item["result"].(map[string]any)
		modelResponded = modelResponded || ok && item["id"] == float64(21) && result["stopReason"] == "end_turn"
	}
	if !modelResponded {
		t.Fatalf("model messages=%#v", modelMessages)
	}
	current.mu.Lock()
	used := current.inputTokens
	current.mu.Unlock()
	if used != 333 {
		t.Fatalf("live input tokens=%d", used)
	}
	output.Reset()
	server.handleSessionAdmin(message{ID: json.RawMessage("22"), Method: "x.ai/session/info", Params: json.RawMessage(`{"sessionId":"status-session"}`)})
	info = decodeACPOutput(t, output.Bytes())[0]["result"].(map[string]any)["result"].(map[string]any)
	contextInfo = info["context"].(map[string]any)
	if contextInfo["used"] != float64(333) || contextInfo["total"] != float64(0) || contextInfo["usagePct"] != float64(0) {
		t.Fatalf("live info=%#v", info)
	}
}

func writeCommandSkill(t *testing.T, root, name, content string) {
	t.Helper()
	dir := filepath.Join(root, ".grok", "skills", name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func quoted(value string) string {
	data, _ := json.Marshal(value)
	return string(data)
}
