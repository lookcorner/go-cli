package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lookcorner/go-cli/internal/api"
	"github.com/lookcorner/go-cli/internal/compat"
	"github.com/lookcorner/go-cli/internal/session"
	"github.com/lookcorner/go-cli/internal/skills"
	"github.com/lookcorner/go-cli/internal/tools"
	"github.com/lookcorner/go-cli/internal/workspace"
)

type fakeStreamer struct {
	requests []api.ResponseRequest
	results  []api.StreamResult
}

type recordingToolObserver struct {
	results []tools.ExecutionResult
}

type denyingHookPolicy struct {
	started int
	prompts []string
	before  []string
	after   []string
	stopped []string
}

func (p *denyingHookPolicy) SessionStarted(context.Context) { p.started++ }
func (p *denyingHookPolicy) UserPromptSubmitted(_ context.Context, prompt string) {
	p.prompts = append(p.prompts, prompt)
}
func (p *denyingHookPolicy) BeforeTool(_ context.Context, call api.ToolCall) error {
	p.before = append(p.before, call.Name)
	return errors.New("blocked by policy")
}
func (p *denyingHookPolicy) AfterTool(_ context.Context, call api.ToolCall, _ tools.ExecutionResult, _ error) {
	p.after = append(p.after, call.Name)
}
func (p *denyingHookPolicy) Stopped(_ context.Context, reason string) {
	p.stopped = append(p.stopped, reason)
}

func (*recordingToolObserver) ToolStarted(api.ToolCall) {}

func (o *recordingToolObserver) ToolFinished(_ api.ToolCall, result tools.ExecutionResult, _ error) {
	o.results = append(o.results, result)
}

func TestRunnerHookPolicyCanDenyBeforeToolExecution(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	streamer := &fakeStreamer{results: []api.StreamResult{
		{ResponseID: "resp_1", ToolCalls: []api.ToolCall{{CallID: "call_1", Name: "shell", Arguments: json.RawMessage(`{"command":"touch should-not-exist"}`)}}},
		{ResponseID: "resp_2", Text: "done"},
	}}
	policy := &denyingHookPolicy{}
	observer := &recordingToolObserver{}
	runner := Runner{
		Client: streamer, Tools: tools.NewRegistry(ws, tools.PromptApprover{Mode: tools.PermissionAuto}),
		HookPolicy: policy, ToolObserver: observer, Model: "test", MaxSteps: 2,
	}
	defer runner.Tools.Close()
	if _, err := runner.Run(context.Background(), "inspect"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(ws.Root(), "should-not-exist")); !os.IsNotExist(err) {
		t.Fatalf("denied tool executed: %v", err)
	}
	if policy.started != 1 || strings.Join(policy.prompts, "|") != "inspect" || strings.Join(policy.before, "|") != "shell" || len(policy.after) != 0 || strings.Join(policy.stopped, "|") != "completed" {
		t.Fatalf("policy=%#v", policy)
	}
	if len(streamer.requests) != 2 || !strings.Contains(streamer.requests[1].Input[0].Output, "blocked by policy") || len(observer.results) != 1 {
		t.Fatalf("requests=%#v observer=%#v", streamer.requests, observer)
	}
}

func (f *fakeStreamer) StreamResponse(_ context.Context, request api.ResponseRequest, onText func(string)) (api.StreamResult, error) {
	f.requests = append(f.requests, request)
	result := f.results[len(f.requests)-1]
	if onText != nil && result.Text != "" {
		onText(result.Text)
	}
	return result, nil
}

func TestRunnerExecutesToolLoop(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("hello agent\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	streamer := &fakeStreamer{results: []api.StreamResult{
		{ResponseID: "resp_1", ToolCalls: []api.ToolCall{{CallID: "call_1", Name: "read_file", Arguments: json.RawMessage(`{"path":"README.md"}`)}}},
		{ResponseID: "resp_2", Text: "done"},
	}}
	var output bytes.Buffer
	runner := Runner{
		Client: streamer,
		Tools:  tools.NewRegistry(ws, tools.PromptApprover{Mode: tools.PermissionDeny}),
		Model:  "test-model", MaxSteps: 3, TextOutput: &output,
	}
	result, err := runner.RunTurn(context.Background(), "inspect the readme", "resp_0")
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "done" || result.Steps != 2 || output.String() != "done" {
		t.Fatalf("unexpected result=%#v output=%q", result, output.String())
	}
	if len(streamer.requests) != 2 {
		t.Fatalf("expected two requests, got %d", len(streamer.requests))
	}
	if streamer.requests[0].PreviousResponseID != "resp_0" {
		t.Fatalf("first request did not continue prior conversation: %#v", streamer.requests[0])
	}
	second := streamer.requests[1]
	if second.PreviousResponseID != "resp_1" || len(second.Input) != 1 {
		t.Fatalf("unexpected continuation: %#v", second)
	}
	if second.Input[0].Type != "function_call_output" || second.Input[0].CallID != "call_1" {
		t.Fatalf("unexpected tool output item: %#v", second.Input[0])
	}
}

func TestRunnerForwardsReadFileImages(t *testing.T) {
	root := t.TempDir()
	toolsTestImage := filepath.Join(root, "screen.png")
	file, err := os.Create(toolsTestImage)
	if err != nil {
		t.Fatal(err)
	}
	pngData := []byte{137, 80, 78, 71, 13, 10, 26, 10, 0, 0, 0, 13, 73, 72, 68, 82, 0, 0, 0, 1, 0, 0, 0, 1, 8, 2, 0, 0, 0, 144, 119, 83, 222, 0, 0, 0, 12, 73, 68, 65, 84, 8, 215, 99, 248, 207, 192, 0, 0, 3, 1, 1, 0, 24, 221, 141, 176, 0, 0, 0, 0, 73, 69, 78, 68, 174, 66, 96, 130}
	if _, err := file.Write(pngData); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	streamer := &fakeStreamer{results: []api.StreamResult{
		{ResponseID: "resp_1", ToolCalls: []api.ToolCall{
			{CallID: "call_1", Name: "read_file", Arguments: json.RawMessage(`{"target_file":"screen.png"}`)},
			{CallID: "call_2", Name: "read_file", Arguments: json.RawMessage(`{"target_file":"screen.png"}`)},
		}},
		{ResponseID: "resp_2", Text: "done"},
	}}
	observer := &recordingToolObserver{}
	runner := Runner{Client: streamer, Tools: tools.NewRegistry(ws, tools.PromptApprover{Mode: tools.PermissionAuto}), ToolObserver: observer, Model: "test", MaxSteps: 2}
	defer runner.Tools.Close()
	if _, err := runner.Run(context.Background(), "inspect image"); err != nil {
		t.Fatal(err)
	}
	input := streamer.requests[1].Input
	if len(input) != 3 || input[0].Type != "function_call_output" || input[1].Type != "function_call_output" || input[2].Type != "message" {
		t.Fatalf("unexpected image continuation: %#v", input)
	}
	parts, ok := input[2].Content.([]api.ContentPart)
	if !ok || len(parts) != 3 || parts[1].Type != "input_image" || parts[2].Type != "input_image" || !strings.HasPrefix(parts[1].ImageURL, "data:image/png;base64,") {
		t.Fatalf("image content was not forwarded: %#v", input[2].Content)
	}
	if len(observer.results) != 2 || len(observer.results[0].Images) != 1 || observer.results[0].Images[0].MediaType != "image/png" {
		t.Fatalf("tool observer lost image attachments: %#v", observer.results)
	}
}

func TestRunnerAcceptsMultimodalPromptParts(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	streamer := &fakeStreamer{results: []api.StreamResult{{ResponseID: "resp_1", Text: "done"}}}
	sessionDir := t.TempDir()
	logger, err := session.NewLoggerWithID(sessionDir, "multimodal")
	if err != nil {
		t.Fatal(err)
	}
	runner := Runner{Client: streamer, Tools: tools.NewRegistry(ws, tools.PromptApprover{Mode: tools.PermissionAuto}), Logger: logger, Model: "test", MaxSteps: 1}
	defer runner.Tools.Close()
	parts := []api.ContentPart{
		{Type: "input_text", Text: "inspect"},
		{Type: "input_image", ImageURL: "https://example.com/image.png"},
	}
	if _, err := runner.RunTurnParts(context.Background(), "inspect image", parts, ""); err != nil {
		t.Fatal(err)
	}
	got, ok := streamer.requests[0].Input[0].Content.([]api.ContentPart)
	if !ok || len(got) != 2 || got[1].ImageURL != parts[1].ImageURL {
		t.Fatalf("multimodal prompt was not preserved: %#v", streamer.requests[0].Input)
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	messages, err := session.Transcript(filepath.Join(sessionDir, "multimodal.jsonl"))
	if err != nil || len(messages) != 2 || len(messages[0].Content) != 2 || messages[0].Content[1].URI != parts[1].ImageURL {
		t.Fatalf("multimodal prompt was not persisted: %#v err=%v", messages, err)
	}
}

func TestRunnerAnnouncesConditionalSkillAfterFileTool(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GROK_HOME", filepath.Join(home, ".grok"))
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	skillDir := filepath.Join(root, ".grok", "skills", "go-guide")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: go-guide\ndescription: Go guide\npaths: ['**/*.go']\n---\nGuide\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := skills.Discover(ws.Root(), skills.Config{Compat: compat.Default()})
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry(ws, tools.PromptApprover{Mode: tools.PermissionAuto})
	defer registry.Close()
	if err := registry.Register(catalog.Tool()); err != nil {
		t.Fatal(err)
	}
	streamer := &fakeStreamer{results: []api.StreamResult{
		{ResponseID: "resp_1", ToolCalls: []api.ToolCall{{CallID: "call_1", Name: "read_file", Arguments: json.RawMessage(`{"path":"main.go"}`)}}},
		{ResponseID: "resp_2", Text: "done"},
	}}
	runner := Runner{Client: streamer, Tools: registry, Skills: catalog, Model: "test", MaxSteps: 2}
	if _, err := runner.Run(context.Background(), "inspect"); err != nil {
		t.Fatal(err)
	}
	if len(streamer.requests[1].Input) != 2 {
		t.Fatalf("expected tool output and skill reminder: %#v", streamer.requests[1].Input)
	}
	reminder, _ := streamer.requests[1].Input[1].Content.(string)
	if !strings.Contains(reminder, "go-guide") {
		t.Fatalf("conditional skill was not announced: %q", reminder)
	}
	found := false
	for _, definition := range streamer.requests[1].Tools {
		if definition.Name == "skill" && strings.Contains(definition.Description, "go-guide") {
			found = true
		}
	}
	if !found {
		t.Fatal("activated skill was absent from the next tool definition")
	}
}

func TestRunnerExpandsUserSkillReferences(t *testing.T) {
	root := t.TempDir()
	skillDir := filepath.Join(root, ".grok", "skills", "commit")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: commit\ndescription: Commit changes\ndisable-model-invocation: true\n---\nCommit $ARGUMENTS in ${SESSION_ID}"), 0o600); err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := skills.Discover(ws.Root(), skills.Config{Compat: compat.Default()})
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry(ws, tools.PromptApprover{Mode: tools.PermissionAuto})
	defer registry.Close()
	streamer := &fakeStreamer{results: []api.StreamResult{{ResponseID: "resp_1", Text: "done"}}}
	runner := Runner{Client: streamer, Tools: registry, Skills: catalog, SessionID: "session-123", Model: "test", MaxSteps: 1}
	if _, err := runner.Run(context.Background(), "/commit fix typo"); err != nil {
		t.Fatal(err)
	}
	content, ok := streamer.requests[0].Input[0].Content.(string)
	if !ok || !strings.Contains(content, "<user_query>\n/commit fix typo\n</user_query>") || !strings.Contains(content, `<skill name="commit" args="fix typo">`) || !strings.Contains(content, "Commit fix typo in session-123") {
		t.Fatalf("skill reference was not expanded into the model request: %#v", streamer.requests[0].Input)
	}
}

func TestRunnerIncludesWatchedSkillInNextRequest(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GROK_HOME", filepath.Join(home, ".grok"))
	root := t.TempDir()
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := skills.Discover(ws.Root(), skills.Config{Compat: compat.Default()})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	catalog.Watch(ctx, 5*time.Millisecond)
	skillDir := filepath.Join(root, ".grok", "skills", "watched")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: watched\ndescription: Watched skill\n---\nInstructions\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for len(catalog.Names()) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	registry := tools.NewRegistry(ws, tools.PromptApprover{Mode: tools.PermissionAuto})
	defer registry.Close()
	if err := registry.Register(catalog.Tool()); err != nil {
		t.Fatal(err)
	}
	streamer := &fakeStreamer{results: []api.StreamResult{{ResponseID: "resp_1", Text: "done"}}}
	runner := Runner{Client: streamer, Tools: registry, Skills: catalog, Model: "test", MaxSteps: 1}
	if _, err := runner.Run(ctx, "inspect"); err != nil {
		t.Fatal(err)
	}
	if len(streamer.requests) != 1 || len(streamer.requests[0].Input) != 2 {
		t.Fatalf("watch reminder missing from request: %#v", streamer.requests)
	}
	reminder, _ := streamer.requests[0].Input[1].Content.(string)
	if !strings.Contains(reminder, "Skills changed on disk") || !strings.Contains(reminder, "watched") {
		t.Fatalf("unexpected watch reminder: %q", reminder)
	}
	found := false
	for _, definition := range streamer.requests[0].Tools {
		if definition.Name == "skill" && strings.Contains(definition.Description, "watched") {
			found = true
		}
	}
	if !found {
		t.Fatal("watched skill was absent from the tool definition")
	}
}

func TestRunnerCompactsAtUsageThresholdAndStartsFreshChain(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	streamer := &fakeStreamer{results: []api.StreamResult{
		{ResponseID: "old-response", Text: "first answer", Usage: api.Usage{InputTokens: 900}},
		{ResponseID: "summary-response", Text: "Preserve the implementation state."},
		{ResponseID: "fresh-response", Text: "continued", Usage: api.Usage{InputTokens: 100}},
	}}
	runner := Runner{
		Client: streamer, Tools: tools.NewRegistry(ws, tools.PromptApprover{Mode: tools.PermissionAuto}),
		Model: "test-model", ContextWindow: 1000, CompactThresholdPercent: 85,
	}
	defer runner.Tools.Close()
	if _, err := runner.RunTurn(context.Background(), "first", ""); err != nil {
		t.Fatal(err)
	}
	result, err := runner.RunTurn(context.Background(), "continue", "old-response")
	if err != nil {
		t.Fatal(err)
	}
	if result.ResponseID != "fresh-response" || len(streamer.requests) != 3 {
		t.Fatalf("unexpected compacted result: %#v requests=%d", result, len(streamer.requests))
	}
	if streamer.requests[1].PreviousResponseID != "old-response" || len(streamer.requests[1].Tools) != 0 {
		t.Fatalf("invalid compaction request: %#v", streamer.requests[1])
	}
	if streamer.requests[2].PreviousResponseID != "" || !strings.Contains(streamer.requests[2].Input[0].Content.(string), "Preserve the implementation state") {
		t.Fatalf("fresh chain did not receive summary: %#v", streamer.requests[2])
	}
}

func TestRunnerManualCompactQueuesSummaryForNextTurn(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	streamer := &fakeStreamer{results: []api.StreamResult{
		{ResponseID: "summary-response", Text: "Keep the exact implementation state."},
		{ResponseID: "fresh-response", Text: "continued"},
	}}
	runner := Runner{Client: streamer, Tools: tools.NewRegistry(ws, tools.PromptApprover{Mode: tools.PermissionAuto}), Model: "test-model"}
	defer runner.Tools.Close()
	if summary, err := runner.Compact(context.Background(), "old-response"); err != nil || summary != "Keep the exact implementation state." {
		t.Fatalf("manual compact: summary=%q err=%v", summary, err)
	}
	if _, err := runner.RunTurn(context.Background(), "continue", ""); err != nil {
		t.Fatal(err)
	}
	if len(streamer.requests) != 2 || streamer.requests[0].PreviousResponseID != "old-response" {
		t.Fatalf("unexpected compact requests: %#v", streamer.requests)
	}
	content, _ := streamer.requests[1].Input[0].Content.(string)
	if streamer.requests[1].PreviousResponseID != "" || !strings.Contains(content, "Keep the exact implementation state.") {
		t.Fatalf("summary was not injected into fresh turn: %#v", streamer.requests[1])
	}
}
