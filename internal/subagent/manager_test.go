package subagent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/agents"
	"github.com/lookcorner/go-cli/internal/api"
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
		NewClient: func(string) (agent.ResponseStreamer, error) { return client, nil }, Observer: observer,
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
	resumeArgs := `{"prompt":"continue","description":"continue","subagent_type":"explore","run_in_background":false,"resume_from":` + strconvQuote(first.ID) + `}`
	secondRaw, err := registry.Execute(context.Background(), "task", json.RawMessage(resumeArgs))
	if err != nil {
		t.Fatal(err)
	}
	var second tools.SubagentResult
	if json.Unmarshal([]byte(secondRaw), &second) != nil || second.Output != "continued" || client.requests[1].PreviousResponseID != "child-1" {
		t.Fatalf("second=%q requests=%#v", secondRaw, client.requests)
	}
	observer.mu.Lock()
	events := strings.Join(observer.events, "|")
	observer.mu.Unlock()
	if !strings.Contains(events, "explore:completed") {
		t.Fatalf("observer events=%q", events)
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
		NewClient: func(string) (agent.ResponseStreamer, error) { return client, nil },
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
	definition := "---\nname: reviewer\ndescription: Review code\ntools: [read_file]\nmaxTurns: 3\n---\nOnly report review findings."
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
	client := &sequenceClient{results: []api.StreamResult{{ResponseID: "review", Text: "clean"}}}
	manager, err := New(Config{Catalog: catalog, Tools: registry, WorkspaceRoot: root, ParentModel: "parent", NewClient: func(string) (agent.ResponseStreamer, error) { return client, nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	if err := registry.SetSubagentBackend(manager); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Execute(context.Background(), "task", json.RawMessage(`{"prompt":"review","description":"review","subagent_type":"reviewer","run_in_background":false}`)); err != nil {
		t.Fatal(err)
	}
	request := client.requests[0]
	if !strings.Contains(request.Instructions, "Only report review findings") || len(request.Tools) != 1 || request.Tools[0].Name != "read_file" {
		t.Fatalf("request=%#v", request)
	}
}

func strconvQuote(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
