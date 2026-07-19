package subagent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/agents"
	"github.com/lookcorner/go-cli/internal/api"
	"github.com/lookcorner/go-cli/internal/hooks"
	"github.com/lookcorner/go-cli/internal/plugin"
	"github.com/lookcorner/go-cli/internal/skills"
	"github.com/lookcorner/go-cli/internal/tools"
	"github.com/lookcorner/go-cli/internal/workspace"
)

type sequenceClient struct {
	mu       sync.Mutex
	results  []api.StreamResult
	requests []api.ResponseRequest
	block    bool
}

func (c *sequenceClient) StreamResponse(ctx context.Context, request api.ResponseRequest, _ func(string)) (api.StreamResult, error) {
	c.mu.Lock()
	c.requests = append(c.requests, request)
	index := len(c.requests) - 1
	block := c.block
	c.mu.Unlock()
	if block {
		<-ctx.Done()
		return api.StreamResult{}, ctx.Err()
	}
	if index >= len(c.results) {
		return api.StreamResult{}, errors.New("missing fixture response")
	}
	return c.results[index], nil
}

type recordingObserver struct {
	mu     sync.Mutex
	events []string
}

type fixtureMCPTool struct {
	name   string
	server string
}

func (t fixtureMCPTool) Definition() api.ToolDefinition {
	return api.ToolDefinition{Type: "function", Name: t.name, Parameters: map[string]any{"type": "object"}}
}
func (t fixtureMCPTool) Execute(context.Context, json.RawMessage) (string, error) { return t.name, nil }
func (t fixtureMCPTool) MCPServerName() string                                    { return t.server }

func (o *recordingObserver) SubagentStarted(_ context.Context, id, agentType, _ string) {
	o.mu.Lock()
	o.events = append(o.events, "start:"+id+":"+agentType)
	o.mu.Unlock()
}

func (o *recordingObserver) SubagentEnded(_ context.Context, id, agentType, status string, _ int64) {
	o.mu.Lock()
	o.events = append(o.events, "end:"+id+":"+agentType+":"+status)
	o.mu.Unlock()
}

func TestTaskToolRunsFilteredSubagentAndResumes(t *testing.T) {
	root := t.TempDir()
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry(ws, tools.PromptApprover{Mode: tools.PermissionAuto})
	defer registry.Close()
	catalog, _ := agents.Discover(agents.Config{})
	client := &sequenceClient{results: []api.StreamResult{
		{ResponseID: "child-1", Text: "found it"},
		{ResponseID: "child-2", Text: "continued"},
	}}
	observer := &recordingObserver{}
	manager, err := New(Config{
		Context: context.Background(), Catalog: catalog, Tools: registry, WorkspaceRoot: root, ParentModel: "parent",
		ContextWindow: 256000, CompactThresholdPercent: 80,
		ResolveModel: func(model string) (ModelRuntime, bool) {
			return ModelRuntime{Profile: model, Model: model, ContextWindow: 256000, CompactThresholdPercent: 80}, model == "parent"
		}, AvailableModels: []string{"parent"},
		NewClient: func(ModelRuntime) (agent.ResponseStreamer, error) { return client, nil }, Observer: observer,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	if err := registry.SetSubagentBackend(manager); err != nil {
		t.Fatal(err)
	}
	firstRaw, err := registry.Execute(context.Background(), "task", json.RawMessage(`{"prompt":"find code","description":"find code","subagent_type":"explore","run_in_background":false}`))
	if err != nil {
		t.Fatal(err)
	}
	var first tools.SubagentResult
	if json.Unmarshal([]byte(firstRaw), &first) != nil || first.Status != "completed" || first.Output != "found it" {
		t.Fatalf("first=%q", firstRaw)
	}
	for _, definition := range client.requests[0].Tools {
		if definition.Name == "task" || definition.Name == "write_file" || definition.Name == "shell" {
			t.Fatalf("explore received disallowed tool %q", definition.Name)
		}
	}
	resumeArgs := `{"prompt":"continue","description":"continue","subagent_type":"explore","run_in_background":false,"model":"missing","resume_from":` + strconvQuote(first.ID) + `}`
	secondRaw, err := registry.Execute(context.Background(), "task", json.RawMessage(resumeArgs))
	if err != nil {
		t.Fatal(err)
	}
	var second tools.SubagentResult
	if json.Unmarshal([]byte(secondRaw), &second) != nil || second.Output != "continued" || client.requests[1].PreviousResponseID != "child-1" {
		t.Fatalf("second=%q requests=%#v", secondRaw, client.requests)
	}
	for _, id := range []string{first.ID, second.ID} {
		runner := manager.tasks[id].runner
		if runner.ContextWindow != 256000 || runner.CompactThresholdPercent != 80 {
			t.Fatalf("task %s context=%d threshold=%d", id, runner.ContextWindow, runner.CompactThresholdPercent)
		}
	}
	observer.mu.Lock()
	events := strings.Join(observer.events, "|")
	observer.mu.Unlock()
	if !strings.Contains(events, "explore:completed") {
		t.Fatalf("observer events=%q", events)
	}
}

func TestSubagentFiltersInheritedMCPServers(t *testing.T) {
	root := t.TempDir()
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry(ws, tools.PromptApprover{Mode: tools.PermissionAuto})
	defer registry.Close()
	for _, tool := range []fixtureMCPTool{
		{name: "mcp__github__read", server: "github"},
		{name: "mcp__slack__search", server: "slack"},
	} {
		if err := registry.Register(tool); err != nil {
			t.Fatal(err)
		}
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GROK_HOME", filepath.Join(home, ".grok"))
	agentDir := filepath.Join(home, ".grok", "agents")
	if err := os.MkdirAll(agentDir, 0o700); err != nil {
		t.Fatal(err)
	}
	agentsByMode := map[string]string{
		"all":    "",
		"none":   "mcpInheritance: none\n",
		"named":  "mcpInheritance:\n  named: [github]\n",
		"except": "mcpInheritance:\n  except: [github]\n",
	}
	for name, inheritance := range agentsByMode {
		content := "---\nname: " + name + "\ndescription: test\n" + inheritance + "---\nTest."
		if err := os.WriteFile(filepath.Join(agentDir, name+".md"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	pluginDir := filepath.Join(t.TempDir(), "agents")
	if err := os.MkdirAll(pluginDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "worker.md"), []byte("---\nname: worker\ndescription: plugin worker\n---\nTest."), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog, loadErrors := agents.Discover(agents.Config{Plugins: []plugin.Plugin{{Name: "fixture", AgentDirs: []string{pluginDir}, Executable: true}}})
	if len(loadErrors) != 0 {
		t.Fatal(loadErrors)
	}
	client := &sequenceClient{results: []api.StreamResult{{Text: "all"}, {Text: "none"}, {Text: "named"}, {Text: "except"}, {Text: "plugin"}}}
	manager, err := New(Config{
		Catalog: catalog, Tools: registry, WorkspaceRoot: root, ParentModel: "parent",
		NewClient: func(ModelRuntime) (agent.ResponseStreamer, error) { return client, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	tests := []struct {
		typeName string
		want     string
	}{
		{typeName: "all", want: "mcp__github__read|mcp__slack__search"},
		{typeName: "none"},
		{typeName: "named", want: "mcp__github__read"},
		{typeName: "except", want: "mcp__slack__search"},
		{typeName: "fixture:worker"},
	}
	for index, test := range tests {
		if _, err := manager.Start(context.Background(), tools.SubagentRequest{Prompt: "test", Description: "test", Type: test.typeName, BackgroundSet: true}); err != nil {
			t.Fatalf("start %s: %v", test.typeName, err)
		}
		var names []string
		for _, definition := range client.requests[index].Tools {
			if strings.HasPrefix(definition.Name, "mcp__") {
				names = append(names, definition.Name)
			}
		}
		if got := strings.Join(names, "|"); got != test.want {
			t.Fatalf("%s MCP tools=%q want=%q", test.typeName, got, test.want)
		}
	}
}

func TestSubagentInlineHooksAreIsolatedSecureAndResume(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	root := t.TempDir()
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry(ws, tools.PromptApprover{Mode: tools.PermissionAuto})
	defer registry.Close()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GROK_HOME", filepath.Join(home, ".grok"))
	capture := filepath.Join(root, "capture.jsonl")
	script := filepath.Join(root, "capture.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ncat >> "+strconvQuote(capture)+"\nprintf '\\n' >> "+strconvQuote(capture)+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	agentDir := filepath.Join(home, ".grok", "agents")
	if err := os.MkdirAll(agentDir, 0o700); err != nil {
		t.Fatal(err)
	}
	hooked := "---\nname: hooked\ndescription: hooked\nhooks:\n  Stop:\n    - hooks:\n        - type: command\n          command: ./capture.sh\n---\nTest."
	if err := os.WriteFile(filepath.Join(agentDir, "hooked.md"), []byte(hooked), 0o600); err != nil {
		t.Fatal(err)
	}
	pluginDir := filepath.Join(t.TempDir(), "agents")
	if err := os.MkdirAll(pluginDir, 0o700); err != nil {
		t.Fatal(err)
	}
	pluginCapture := filepath.Join(root, "plugin-capture")
	pluginAgent := "---\nname: worker\ndescription: worker\nhooks:\n  Stop:\n    - hooks:\n        - type: command\n          command: touch " + pluginCapture + "\n---\nTest."
	if err := os.WriteFile(filepath.Join(pluginDir, "worker.md"), []byte(pluginAgent), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog, loadErrors := agents.Discover(agents.Config{Plugins: []plugin.Plugin{{Name: "fixture", AgentDirs: []string{pluginDir}, Executable: true}}})
	if len(loadErrors) != 0 {
		t.Fatal(loadErrors)
	}
	hookCatalog := hooks.Discover(hooks.Config{WorkspaceRoot: root, ProjectTrusted: true})
	if len(hookCatalog.Snapshot().Hooks) != 0 {
		t.Fatal("test hook catalog unexpectedly discovered hooks")
	}
	client := &sequenceClient{results: []api.StreamResult{
		{ResponseID: "hook-1", Text: "first"},
		{ResponseID: "hook-2", Text: "second"},
		{ResponseID: "hook-3", Text: "plugin"},
	}}
	manager, err := New(Config{
		Catalog: catalog, Tools: registry, Hooks: hookCatalog, WorkspaceRoot: root, ParentModel: "parent",
		NewClient: func(ModelRuntime) (agent.ResponseStreamer, error) { return client, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	first, err := manager.Start(context.Background(), tools.SubagentRequest{Prompt: "first", Description: "first", Type: "hooked", BackgroundSet: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Start(context.Background(), tools.SubagentRequest{Prompt: "second", Description: "second", Type: "hooked", ResumeFrom: first.ID, BackgroundSet: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Start(context.Background(), tools.SubagentRequest{Prompt: "plugin", Description: "plugin", Type: "fixture:worker", BackgroundSet: true}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(capture)
	if err != nil || strings.Count(string(data), `"hookEventName":"subagent_stop"`) != 2 || strings.Count(string(data), `"subagentType":"hooked"`) != 2 {
		t.Fatalf("capture=%q err=%v", data, err)
	}
	if _, err := os.Stat(pluginCapture); !os.IsNotExist(err) {
		t.Fatalf("plugin inline hook executed: %v", err)
	}
	if len(hookCatalog.Snapshot().Hooks) != 0 {
		t.Fatal("child inline hooks mutated parent catalog")
	}
}

func TestBackgroundSubagentPollAndKill(t *testing.T) {
	root := t.TempDir()
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry(ws, tools.PromptApprover{Mode: tools.PermissionAuto})
	defer registry.Close()
	catalog, _ := agents.Discover(agents.Config{})
	client := &sequenceClient{block: true}
	manager, err := New(Config{
		Context: context.Background(), Catalog: catalog, Tools: registry, WorkspaceRoot: root,
		NewClient: func(ModelRuntime) (agent.ResponseStreamer, error) { return client, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	if err := registry.SetSubagentBackend(manager); err != nil {
		t.Fatal(err)
	}
	startedRaw, err := registry.Execute(context.Background(), "task", json.RawMessage(`{"prompt":"wait","description":"wait","subagent_type":"general-purpose"}`))
	if err != nil {
		t.Fatal(err)
	}
	var started tools.SubagentResult
	if json.Unmarshal([]byte(startedRaw), &started) != nil || started.Status != "running" {
		t.Fatalf("started=%q", startedRaw)
	}
	poll, err := registry.Execute(context.Background(), "get_task_output", json.RawMessage(`{"task_ids":[`+strconvQuote(started.ID)+`]}`))
	if err != nil || !strings.Contains(poll, `"status":"running"`) {
		t.Fatalf("poll=%q err=%v", poll, err)
	}
	if _, err := registry.Execute(context.Background(), "kill_task", json.RawMessage(`{"task_id":`+strconvQuote(started.ID)+`}`)); err != nil {
		t.Fatal(err)
	}
	output, err := manager.Output(context.Background(), started.ID, time.Second)
	if err != nil || output.Status != "cancelled" {
		t.Fatalf("output=%#v err=%v", output, err)
	}
}

func TestTaskToolExecutesUserAgentDefinition(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GROK_HOME", filepath.Join(home, ".grok"))
	agentDir := filepath.Join(home, ".grok", "agents")
	if err := os.MkdirAll(agentDir, 0o700); err != nil {
		t.Fatal(err)
	}
	definition := "---\nname: reviewer\ndescription: Review code\ntools: [read_file]\nmaxTurns: 3\nmodel: role-model\neffort: high\ninitialPrompt: do not prepend this\n---\nOnly report review findings."
	if err := os.WriteFile(filepath.Join(agentDir, "reviewer.md"), []byte(definition), 0o600); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry(ws, tools.PromptApprover{Mode: tools.PermissionAuto})
	defer registry.Close()
	catalog, loadErrors := agents.Discover(agents.Config{})
	if len(loadErrors) != 0 {
		t.Fatal(loadErrors)
	}
	client := &sequenceClient{results: []api.StreamResult{{ResponseID: "review", Text: "clean"}, {ResponseID: "fallback", Text: "fallback"}}}
	var createdModels, createdProfiles []string
	manager, err := New(Config{
		Catalog: catalog, Tools: registry, WorkspaceRoot: root, ParentModel: "parent",
		ResolveModel: func(model string) (ModelRuntime, bool) {
			switch model {
			case "parent":
				return ModelRuntime{Profile: "parent", Model: "parent", ContextWindow: 256000, CompactThresholdPercent: 80}, true
			case "fast":
				return ModelRuntime{Profile: "fast", Model: "fast-internal", ContextWindow: 64000, CompactThresholdPercent: 75}, true
			default:
				return ModelRuntime{}, false
			}
		}, AvailableModels: []string{"fast", "parent"},
		NewClient: func(model ModelRuntime) (agent.ResponseStreamer, error) {
			createdModels = append(createdModels, model.Model)
			createdProfiles = append(createdProfiles, model.Profile)
			return client, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	if err := registry.SetSubagentBackend(manager); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Execute(context.Background(), "task", json.RawMessage(`{"prompt":"review","description":"review","subagent_type":"reviewer","run_in_background":false,"model":"fast","reasoning_effort":"low"}`)); err != nil {
		t.Fatal(err)
	}
	request := client.requests[0]
	if len(createdModels) != 1 || createdModels[0] != "fast-internal" || createdProfiles[0] != "fast" || !strings.Contains(request.Instructions, "Only report review findings") || len(request.Tools) != 1 || request.Tools[0].Name != "read_file" || request.Reasoning == nil || request.Reasoning.Effort != "low" || request.Input[0].Content != "review" {
		t.Fatalf("request=%#v", request)
	}
	foundFast := false
	for _, current := range manager.tasks {
		if current.runner.Model == "fast-internal" {
			foundFast = true
			if current.runner.ContextWindow != 64000 || current.runner.CompactThresholdPercent != 75 {
				t.Fatalf("fast runtime context=%d threshold=%d", current.runner.ContextWindow, current.runner.CompactThresholdPercent)
			}
		}
	}
	if !foundFast {
		t.Fatal("fast runtime task not found")
	}
	fallback, err := manager.Start(context.Background(), tools.SubagentRequest{Prompt: "fallback", Description: "fallback", Type: "reviewer", BackgroundSet: true})
	if err != nil || fallback.Output != "fallback" || len(createdModels) != 2 || createdModels[1] != "parent" {
		t.Fatalf("fallback=%#v models=%#v err=%v", fallback, createdModels, err)
	}
	if _, err := manager.Start(context.Background(), tools.SubagentRequest{Prompt: "x", Description: "x", Type: "reviewer", Model: "missing"}); err == nil || !strings.Contains(err.Error(), "valid model slugs: fast, parent") {
		t.Fatalf("unknown model error=%v", err)
	}
}

func TestBypassPermissionsAgentFailsExplicitly(t *testing.T) {
	root := t.TempDir()
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry(ws, tools.PromptApprover{Mode: tools.PermissionAuto})
	defer registry.Close()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GROK_HOME", filepath.Join(home, ".grok"))
	agentDir := filepath.Join(home, ".grok", "agents")
	if err := os.MkdirAll(agentDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(agentDir, "unsafe.md")
	if err := os.WriteFile(path, []byte("---\nname: unsafe\ndescription: unsafe\npermissionMode: bypassPermissions\n---\nUnsafe"), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog, loadErrors := agents.Discover(agents.Config{})
	if len(loadErrors) != 0 {
		t.Fatal(loadErrors)
	}
	manager, err := New(Config{Catalog: catalog, Tools: registry, WorkspaceRoot: root, NewClient: func(ModelRuntime) (agent.ResponseStreamer, error) { return &sequenceClient{}, nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	if _, err := manager.Start(context.Background(), tools.SubagentRequest{Prompt: "x", Description: "x", Type: "unsafe"}); err == nil || !strings.Contains(err.Error(), "not enabled") {
		t.Fatalf("bypass result=%v", err)
	}
}

func TestSubagentSkillsAreClonedAndResumeKeepsChildState(t *testing.T) {
	root := t.TempDir()
	skillRoot := filepath.Join(t.TempDir(), "skills")
	skillDir := filepath.Join(skillRoot, "go-files")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: go-files\ndescription: Go guidance\npaths: ['src/main.go']\n---\nUse Go guidance."
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	alwaysDir := filepath.Join(skillRoot, "always")
	if err := os.MkdirAll(alwaysDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(alwaysDir, "SKILL.md"), []byte("---\nname: always\ndescription: Always guidance\n---\nAlways instructions."), 0o600); err != nil {
		t.Fatal(err)
	}
	localDir := filepath.Join(root, ".grok", "skills", "local")
	if err := os.MkdirAll(localDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(localDir, "SKILL.md"), []byte("---\nname: local\ndescription: Local guidance\n---\nLocal instructions."), 0o600); err != nil {
		t.Fatal(err)
	}
	parentSkills, err := skills.Discover(root, skills.Config{Paths: []string{skillRoot}})
	if err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry(ws, tools.PromptApprover{Mode: tools.PermissionAuto})
	defer registry.Close()
	if err := registry.Register(parentSkills.Tool()); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GROK_HOME", filepath.Join(home, ".grok"))
	agentDir := filepath.Join(home, ".grok", "agents")
	if err := os.MkdirAll(agentDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "skilled.md"), []byte("---\nname: skilled\ndescription: skilled\ntools: [read_file, skill]\nskills: [always]\n---\nUse skills."), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "no-skills.md"), []byte("---\nname: no-skills\ndescription: no skills\ntools: [skill]\ndiscoverSkills: false\n---\nNo skills."), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "fresh-skills.md"), []byte("---\nname: fresh-skills\ndescription: fresh skills\ntools: [skill]\ninheritSkills: false\n---\nFresh skills."), 0o600); err != nil {
		t.Fatal(err)
	}
	agentCatalog, loadErrors := agents.Discover(agents.Config{})
	if len(loadErrors) != 0 {
		t.Fatal(loadErrors)
	}
	client := &sequenceClient{results: []api.StreamResult{{ResponseID: "skills-1", Text: "first"}, {ResponseID: "skills-2", Text: "second"}, {ResponseID: "skills-3", Text: "third"}, {ResponseID: "skills-4", Text: "fourth"}}}
	manager, err := New(Config{
		Catalog: agentCatalog, Tools: registry, Skills: parentSkills, SkillConfig: skills.Config{Paths: []string{skillRoot}}, WorkspaceRoot: root, ParentModel: "parent",
		NewClient: func(ModelRuntime) (agent.ResponseStreamer, error) { return client, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	first, err := manager.Start(context.Background(), tools.SubagentRequest{Prompt: "inspect", Description: "inspect", Type: "skilled", BackgroundSet: true})
	if err != nil {
		t.Fatal(err)
	}
	childSkills := manager.tasks[first.ID].runner.Skills
	if childSkills == nil || childSkills == parentSkills {
		t.Fatalf("child skills=%p parent=%p", childSkills, parentSkills)
	}
	if !strings.Contains(client.requests[0].Instructions, `<skill name="always"`) || !strings.Contains(client.requests[0].Instructions, "Always instructions") {
		t.Fatalf("preloaded instructions=%q", client.requests[0].Instructions)
	}
	if reminder := childSkills.Activate("read_file", json.RawMessage(`{"path":"src/main.go"}`)); !strings.Contains(reminder, "go-files") {
		t.Fatalf("child activation=%q", reminder)
	}
	if reminder := parentSkills.Activate("read_file", json.RawMessage(`{"path":"src/main.go"}`)); !strings.Contains(reminder, "go-files") {
		t.Fatalf("parent was polluted by child activation: %q", reminder)
	}
	childToolVisible := false
	for _, definition := range manager.tasks[first.ID].runner.Tools.Definitions() {
		if definition.Name == "skill" && strings.Contains(definition.Description, "go-files") && !strings.Contains(definition.Description, "always") {
			childToolVisible = true
		}
	}
	if !childToolVisible {
		t.Fatal("child skill tool did not use cloned catalog")
	}
	second, err := manager.Start(context.Background(), tools.SubagentRequest{
		Prompt: "continue", Description: "continue", Type: "skilled", ResumeFrom: first.ID, BackgroundSet: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if manager.tasks[second.ID].runner.Skills != childSkills {
		t.Fatal("resume replaced child skill state")
	}
	third, err := manager.Start(context.Background(), tools.SubagentRequest{Prompt: "none", Description: "none", Type: "no-skills", BackgroundSet: true})
	if err != nil {
		t.Fatal(err)
	}
	if manager.tasks[third.ID].runner.Skills != nil || manager.tasks[third.ID].runner.Tools.HasTool("skill") {
		t.Fatal("discoverSkills=false retained skill state or tool")
	}
	fourth, err := manager.Start(context.Background(), tools.SubagentRequest{Prompt: "fresh", Description: "fresh", Type: "fresh-skills", BackgroundSet: true})
	if err != nil {
		t.Fatal(err)
	}
	definitions := manager.tasks[fourth.ID].runner.Tools.Definitions()
	if len(definitions) != 1 || definitions[0].Name != "skill" || !strings.Contains(definitions[0].Description, "local") || strings.Contains(definitions[0].Description, "always") {
		t.Fatalf("fresh skill definitions=%#v", definitions)
	}
}

func strconvQuote(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
