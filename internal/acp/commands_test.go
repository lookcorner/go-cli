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
	"github.com/lookcorner/go-cli/internal/skills"
	"github.com/lookcorner/go-cli/internal/tools"
	"github.com/lookcorner/go-cli/internal/workspace"
)

func TestCommandsListAdvertisesCapabilitiesAndSkills(t *testing.T) {
	root := t.TempDir()
	userRoot := t.TempDir()
	writeCommandSkill(t, root, "deploy", "---\nname: deploy\ndescription: Deploy the service\nuser-invocable: true\nargument-hint: environment\nmetadata:\n  short-description: Safe deploy\n---\nDeploy it.\n")
	writeCommandSkill(t, root, "compact", "---\nname: compact\ndescription: Skill compact\nuser-invocable: true\n---\nCompact it.\n")
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
	runner := &agent.Runner{Tools: registry, Skills: catalog}
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
	for _, name := range []string{"compact", "loop", "local:compact", "deploy"} {
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
	if len(commands) != 1 || commands[0].(map[string]any)["name"] != "compact" || commands[0].(map[string]any)["input"].(map[string]any)["hint"] == "" {
		t.Fatalf("available commands=%#v", commands)
	}
	routedCommands := messages[1]["result"].(map[string]any)["commands"].([]any)
	if len(routedCommands) != 1 || routedCommands[0].(map[string]any)["name"] != "compact" {
		t.Fatalf("routed commands=%#v", routedCommands)
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
