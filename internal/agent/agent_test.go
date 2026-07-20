package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

type prefireStreamer struct {
	mu             sync.Mutex
	requests       []api.ResponseRequest
	prefireStarted chan struct{}
	releasePrefire chan struct{}
	startedOnce    sync.Once
	releaseOnce    sync.Once
}

func newPrefireStreamer() *prefireStreamer {
	return &prefireStreamer{prefireStarted: make(chan struct{}), releasePrefire: make(chan struct{})}
}

func (f *prefireStreamer) StreamResponse(ctx context.Context, request api.ResponseRequest, _ func(string)) (api.StreamResult, error) {
	f.mu.Lock()
	f.requests = append(f.requests, request)
	f.mu.Unlock()
	content, _ := request.Input[0].Content.(string)
	switch {
	case strings.Contains(request.Instructions, "first-stage conversation summary"):
		f.startedOnce.Do(func() { close(f.prefireStarted) })
		select {
		case <-ctx.Done():
			return api.StreamResult{}, ctx.Err()
		case <-f.releasePrefire:
			return api.StreamResult{ResponseID: "prefire", Text: "Prefix decisions and constraints."}, nil
		}
	case content == "first":
		return api.StreamResult{ResponseID: "old-response", Text: "first answer", Usage: api.Usage{InputTokens: 750}}, nil
	case content == "continue":
		select {
		case <-ctx.Done():
			return api.StreamResult{}, ctx.Err()
		case <-f.prefireStarted:
			f.releaseOnce.Do(func() { close(f.releasePrefire) })
			return api.StreamResult{ResponseID: "tail-response", Text: "tail answer", Usage: api.Usage{InputTokens: 860}}, nil
		}
	case strings.Contains(content, "final pass of hierarchical compaction"):
		return api.StreamResult{ResponseID: "summary-response", Text: "Merged prefix and tail."}, nil
	case strings.Contains(content, "Previous conversation summary:"):
		return api.StreamResult{ResponseID: "fresh-response", Text: "continued", Usage: api.Usage{InputTokens: 100}}, nil
	default:
		return api.StreamResult{}, fmt.Errorf("unexpected request content %q", content)
	}
}

func (f *prefireStreamer) snapshot() []api.ResponseRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]api.ResponseRequest(nil), f.requests...)
}

type statefulFakeStreamer struct{ *fakeStreamer }

func (*statefulFakeStreamer) ResetHistory(string) {}

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

type lifecycleHookPolicy struct {
	stopErr error
	events  []string
}

func (*lifecycleHookPolicy) SessionStarted(context.Context)                 {}
func (*lifecycleHookPolicy) UserPromptSubmitted(context.Context, string)    {}
func (*lifecycleHookPolicy) BeforeTool(context.Context, api.ToolCall) error { return nil }
func (*lifecycleHookPolicy) AfterTool(context.Context, api.ToolCall, tools.ExecutionResult, error) {
}
func (p *lifecycleHookPolicy) Stopped(_ context.Context, reason string, err error) {
	p.events = append(p.events, "stop:"+reason)
	p.stopErr = err
}
func (p *lifecycleHookPolicy) BeforeCompact(_ context.Context, source string) {
	p.events = append(p.events, "pre:"+source)
}
func (p *lifecycleHookPolicy) AfterCompact(_ context.Context, source string) {
	p.events = append(p.events, "post:"+source)
}

type failingStreamer struct{ err error }

func (f failingStreamer) StreamResponse(context.Context, api.ResponseRequest, func(string)) (api.StreamResult, error) {
	return api.StreamResult{}, f.err
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
func (p *denyingHookPolicy) Stopped(_ context.Context, reason string, _ error) {
	p.stopped = append(p.stopped, reason)
}
func (*denyingHookPolicy) BeforeCompact(context.Context, string) {}
func (*denyingHookPolicy) AfterCompact(context.Context, string)  {}

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

func TestRunnerReportsFailureAndCompactionHookLifecycle(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	policy := &lifecycleHookPolicy{}
	runner := Runner{
		Client: failingStreamer{err: errors.New("model unavailable")},
		Tools:  tools.NewRegistry(ws, tools.PromptApprover{Mode: tools.PermissionAuto}), HookPolicy: policy,
	}
	defer runner.Tools.Close()
	if _, err := runner.Run(context.Background(), "inspect"); err == nil {
		t.Fatal("model failure was lost")
	}
	if policy.stopErr == nil || strings.Join(policy.events, "|") != "stop:failed" {
		t.Fatalf("policy=%#v", policy)
	}

	policy.events = nil
	streamer := &fakeStreamer{results: []api.StreamResult{{ResponseID: "summary", Text: "state"}}}
	runner.Client, runner.HookPolicy = streamer, policy
	if _, err := runner.Compact(context.Background(), "previous"); err != nil {
		t.Fatal(err)
	}
	if strings.Join(policy.events, "|") != "pre:manual|post:manual" {
		t.Fatalf("compact events=%#v", policy.events)
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

func TestRunnerAppliesPlanModeChangesWithinToolLoop(t *testing.T) {
	tests := []struct {
		name        string
		initialPlan bool
		tool        string
		firstPlan   bool
		secondPlan  bool
	}{
		{name: "enter", tool: "enter_plan_mode", secondPlan: true},
		{name: "exit", initialPlan: true, tool: "exit_plan_mode", firstPlan: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ws, err := workspace.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			registry := tools.NewRegistry(ws, tools.PromptApprover{Mode: tools.PermissionAuto})
			defer registry.Close()
			if err := registry.SetPlanMode(tt.initialPlan); err != nil {
				t.Fatal(err)
			}
			streamer := &fakeStreamer{results: []api.StreamResult{
				{ResponseID: "resp_1", ToolCalls: []api.ToolCall{{CallID: "call_1", Name: tt.tool, Arguments: json.RawMessage(`{}`)}}},
				{ResponseID: "resp_2", Text: "done"},
			}}
			runner := Runner{Client: streamer, Tools: registry, Model: "test", MaxSteps: 2}
			if _, err := runner.Run(context.Background(), "plan this"); err != nil {
				t.Fatal(err)
			}
			if len(streamer.requests) != 2 {
				t.Fatalf("requests=%d", len(streamer.requests))
			}
			const marker = "Plan mode is active."
			if strings.Contains(streamer.requests[0].Instructions, marker) != tt.firstPlan || strings.Contains(streamer.requests[1].Instructions, marker) != tt.secondPlan {
				t.Fatalf("plan instructions before=%q after=%q", streamer.requests[0].Instructions, streamer.requests[1].Instructions)
			}
		})
	}
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
		{ResponseID: "resp_1", Usage: api.Usage{InputTokens: 10, OutputTokens: 2, TotalTokens: 12}, ToolCalls: []api.ToolCall{{CallID: "call_1", Name: "read_file", Arguments: json.RawMessage(`{"path":"README.md"}`)}}},
		{ResponseID: "resp_2", Text: "done", Usage: api.Usage{InputTokens: 15, OutputTokens: 3, TotalTokens: 18}},
	}}
	var output bytes.Buffer
	var progress Progress
	runner := Runner{
		Client: streamer,
		Tools:  tools.NewRegistry(ws, tools.PromptApprover{Mode: tools.PermissionDeny}),
		Model:  "test-model", MaxSteps: 3, TextOutput: &output,
		Progress: func(value Progress) { progress = value },
	}
	result, err := runner.RunTurn(context.Background(), "inspect the readme", "resp_0")
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "done" || result.Steps != 2 || result.InputTokens != 15 || result.TokensUsed != 30 || result.ToolCalls != 1 || strings.Join(result.ToolsUsed, "|") != "read_file" || result.ErrorCount != 0 || output.String() != "done" {
		t.Fatalf("unexpected result=%#v output=%q", result, output.String())
	}
	if progress.Turns != 2 || progress.TokensUsed != 30 || progress.InputTokens != 15 || progress.ToolCalls != 1 || strings.Join(progress.ToolsUsed, "|") != "read_file" {
		t.Fatalf("progress=%#v", progress)
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

func TestRunnerTwoPassPrefireMergesOnlyRecentTail(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	streamer := newPrefireStreamer()
	runner := Runner{
		Client: streamer, Tools: tools.NewRegistry(ws, tools.PromptApprover{Mode: tools.PermissionAuto}),
		Model: "test-model", ContextWindow: 1000, CompactThresholdPercent: 85, TwoPassCompaction: true,
	}
	defer runner.Tools.Close()
	if _, err := runner.RunTurn(context.Background(), "first", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.RunTurn(context.Background(), "continue", "old-response"); err != nil {
		t.Fatal(err)
	}
	result, err := runner.RunTurn(context.Background(), "next", "tail-response")
	if err != nil || result.ResponseID != "fresh-response" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	var pass1, pass2 *api.ResponseRequest
	requests := streamer.snapshot()
	for index := range requests {
		request := requests[index]
		content, _ := request.Input[0].Content.(string)
		if strings.Contains(request.Instructions, "first-stage conversation summary") {
			copy := request
			pass1 = &copy
		}
		if strings.Contains(content, "final pass of hierarchical compaction") {
			copy := request
			pass2 = &copy
		}
	}
	if pass1 == nil || pass1.PreviousResponseID != "old-response" {
		t.Fatalf("prefire request=%#v", pass1)
	}
	if pass2 == nil || pass2.PreviousResponseID != "" {
		t.Fatalf("pass2 request=%#v", pass2)
	}
	content, _ := pass2.Input[0].Content.(string)
	if !strings.Contains(content, "Prefix decisions and constraints.") || !strings.Contains(content, "User: continue") || !strings.Contains(content, "Assistant: tail answer") {
		t.Fatalf("pass2 lost prefix or tail: %q", content)
	}
}

func TestRunnerTwoPassFailureFallsBackAndStatefulClientsSkipPrefire(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Run("pass2 fallback", func(t *testing.T) {
		streamer := &fakeStreamer{results: []api.StreamResult{{ResponseID: "bad", Text: ""}, {ResponseID: "summary", Text: "single pass summary"}}}
		runner := Runner{Client: streamer, Tools: tools.NewRegistry(ws, tools.PromptApprover{Mode: tools.PermissionAuto}), Model: "test"}
		defer runner.Tools.Close()
		done := make(chan struct{})
		close(done)
		runner.prefire = &compactionPrefire{done: done, model: "test", lastResponseID: "current", note: "cached prefix"}
		if summary, err := runner.Compact(context.Background(), "current"); err != nil || summary != "single pass summary" {
			t.Fatalf("summary=%q err=%v", summary, err)
		}
		if len(streamer.requests) != 2 || streamer.requests[0].PreviousResponseID != "" || streamer.requests[1].PreviousResponseID != "current" {
			t.Fatalf("requests=%#v", streamer.requests)
		}
	})
	t.Run("stateful client", func(t *testing.T) {
		streamer := &statefulFakeStreamer{fakeStreamer: &fakeStreamer{results: []api.StreamResult{{ResponseID: "next", Text: "done"}}}}
		runner := Runner{
			Client: streamer, Tools: tools.NewRegistry(ws, tools.PromptApprover{Mode: tools.PermissionAuto}), Model: "test",
			ContextWindow: 1000, CompactThresholdPercent: 85, TwoPassCompaction: true, lastInputTokens: 750,
		}
		defer runner.Tools.Close()
		if _, err := runner.RunTurn(context.Background(), "continue", "old"); err != nil {
			t.Fatal(err)
		}
		if len(streamer.requests) != 1 || runner.prefire != nil {
			t.Fatalf("stateful client prefired: requests=%d prefire=%#v", len(streamer.requests), runner.prefire)
		}
	})
	t.Run("multimodal tail", func(t *testing.T) {
		streamer := &fakeStreamer{results: []api.StreamResult{{ResponseID: "next", Text: "done"}}}
		runner := Runner{
			Client: streamer, Tools: tools.NewRegistry(ws, tools.PromptApprover{Mode: tools.PermissionAuto}), Model: "test",
			ContextWindow: 1000, CompactThresholdPercent: 85, TwoPassCompaction: true, lastInputTokens: 750,
		}
		defer runner.Tools.Close()
		done := make(chan struct{})
		close(done)
		runner.prefire = &compactionPrefire{done: done, model: "test", lastResponseID: "old", note: "cached prefix"}
		parts := []api.ContentPart{{Type: "input_text", Text: "inspect"}, {Type: "input_image", ImageURL: "data:image/png;base64,AA=="}}
		if _, err := runner.RunTurnParts(context.Background(), "inspect image", parts, "old"); err != nil {
			t.Fatal(err)
		}
		if runner.prefire != nil || len(streamer.requests) != 1 {
			t.Fatalf("multimodal tail reused prefire: prefire=%#v requests=%d", runner.prefire, len(streamer.requests))
		}
	})
	t.Run("stale response chain", func(t *testing.T) {
		streamer := &fakeStreamer{results: []api.StreamResult{{ResponseID: "summary", Text: "single pass"}}}
		runner := Runner{Client: streamer, Tools: tools.NewRegistry(ws, tools.PromptApprover{Mode: tools.PermissionAuto}), Model: "test"}
		defer runner.Tools.Close()
		done := make(chan struct{})
		close(done)
		runner.prefire = &compactionPrefire{done: done, model: "test", lastResponseID: "other", note: "stale prefix"}
		if _, err := runner.Compact(context.Background(), "current"); err != nil {
			t.Fatal(err)
		}
		if len(streamer.requests) != 1 || streamer.requests[0].PreviousResponseID != "current" {
			t.Fatalf("stale prefire was used: %#v", streamer.requests)
		}
	})
	t.Run("bounded prefire note", func(t *testing.T) {
		streamer := &fakeStreamer{results: []api.StreamResult{{ResponseID: "prefire", Text: strings.Repeat("界", compactionPrefireMaxChars+10)}}}
		runner := Runner{Client: streamer, Model: "test"}
		prefire := &compactionPrefire{done: make(chan struct{}), model: "test"}
		runner.prefire = prefire
		runner.runCompactionPrefire(context.Background(), prefire, "old")
		if got := len([]rune(prefire.note)); got != compactionPrefireMaxChars {
			t.Fatalf("prefire note chars=%d", got)
		}
	})
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
