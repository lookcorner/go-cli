package agent

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lookcorner/go-cli/internal/api"
	"github.com/lookcorner/go-cli/internal/memory"
	"github.com/lookcorner/go-cli/internal/session"
	"github.com/lookcorner/go-cli/internal/tools"
	"github.com/lookcorner/go-cli/internal/workspace"
)

type memoryFlushStreamer struct {
	mu        sync.Mutex
	requests  []api.ResponseRequest
	normal    int
	flushDone chan struct{}
	flushOnce sync.Once
}

type memoryRewriteStreamer struct {
	request        api.ResponseRequest
	includeHistory *bool
	result         api.StreamResult
	err            error
}

func TestRunnerTogglesMemoryAndToolsForSession(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	store, err := memory.Open(root, ws.Root(), "toggle")
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry(ws, tools.PromptApprover{Mode: tools.PermissionAuto})
	defer registry.Close()
	cfg := memory.DefaultConfig()
	cfg.Enabled = true
	if err := tools.RegisterMemoryTools(registry, store, cfg); err != nil {
		t.Fatal(err)
	}
	opened := 0
	runner := Runner{Tools: registry, Memory: store, MemoryConfig: cfg, OpenMemory: func() (*memory.Store, error) {
		opened++
		return memory.Open(root, ws.Root(), "toggle")
	}}
	message, err := runner.SetMemoryEnabled(context.Background(), false)
	if err != nil || message != "Memory disabled for this session." || runner.Memory != nil || runner.MemoryConfig.Enabled || registry.HasTool("memory_search") || registry.HasTool("memory_get") {
		t.Fatalf("disable message=%q err=%v runner=%#v", message, err, runner.MemoryConfig)
	}
	if _, err := runner.ListMemory(); err == nil {
		t.Fatal("disabled memory remained readable")
	}
	message, err = runner.SetMemoryEnabled(context.Background(), true)
	if err != nil || message != "Memory enabled for this session." || opened != 1 || runner.Memory == nil || !runner.MemoryConfig.Enabled || !registry.HasTool("memory_search") || !registry.HasTool("memory_get") {
		t.Fatalf("enable message=%q opened=%d err=%v", message, opened, err)
	}
	if message, err = runner.SetMemoryEnabled(context.Background(), true); err != nil || message != "Memory is already enabled." || opened != 1 {
		t.Fatalf("idempotent message=%q opened=%d err=%v", message, opened, err)
	}
	unconfigured := Runner{MemoryConfig: memory.DefaultConfig()}
	if message, err = unconfigured.SetMemoryEnabled(context.Background(), true); err != nil || message != "Memory cannot be enabled (not configured for this session)." || unconfigured.Memory != nil || unconfigured.MemoryConfig.Enabled {
		t.Fatalf("unconfigured message=%q err=%v", message, err)
	}
}

func (s *memoryRewriteStreamer) CloneForCompaction(includeHistory bool) api.Streamer {
	s.includeHistory = &includeHistory
	return s
}

func (s *memoryRewriteStreamer) StreamResponse(_ context.Context, request api.ResponseRequest, _ func(string)) (api.StreamResult, error) {
	s.request = request
	return s.result, s.err
}

func (s *memoryFlushStreamer) StreamResponse(_ context.Context, request api.ResponseRequest, _ func(string)) (api.StreamResult, error) {
	s.mu.Lock()
	s.requests = append(s.requests, request)
	s.mu.Unlock()
	if strings.Contains(request.Instructions, "memory assistant") {
		s.flushOnce.Do(func() { close(s.flushDone) })
		return api.StreamResult{ResponseID: "flush", Text: "## Decisions\n\nKeep the memory boundary small."}, nil
	}
	s.mu.Lock()
	s.normal++
	turn := s.normal
	s.mu.Unlock()
	return api.StreamResult{ResponseID: []string{"old", "next"}[turn-1], Text: "done", Usage: api.Usage{InputTokens: 4500 + (turn-1)*100}}, nil
}

func (s *memoryFlushStreamer) snapshot() []api.ResponseRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]api.ResponseRequest(nil), s.requests...)
}

func TestRunnerFlushesAndInjectsMemoryAcrossSessions(t *testing.T) {
	root, workspaceRoot := t.TempDir(), t.TempDir()
	store, err := memory.Open(root, workspaceRoot, "first-session")
	if err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Open(workspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	streamer := &memoryFlushStreamer{flushDone: make(chan struct{})}
	config := memory.DefaultConfig()
	config.Enabled = true
	runner := Runner{
		Client: streamer, Tools: tools.NewRegistry(ws, tools.PromptApprover{Mode: tools.PermissionAuto}), Model: "test",
		ContextWindow: 10_000, CompactThresholdPercent: 85, Memory: store, MemoryConfig: config,
	}
	defer runner.Tools.Close()
	if _, err := runner.RunTurn(context.Background(), "first", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.RunTurn(context.Background(), "continue", "old"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-streamer.flushDone:
	case <-time.After(time.Second):
		t.Fatal("memory flush did not start at the headroom threshold")
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := runner.WaitMemory(waitCtx); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		value, err := store.Context()
		if err == nil && strings.Contains(value, "Keep the memory boundary small") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("memory was not written: context=%q err=%v", value, err)
		}
		time.Sleep(time.Millisecond)
	}
	var flushRequest *api.ResponseRequest
	for _, request := range streamer.snapshot() {
		if strings.Contains(request.Instructions, "memory assistant") {
			copy := request
			flushRequest = &copy
		}
	}
	if flushRequest == nil || flushRequest.PreviousResponseID != "old" || len(flushRequest.Tools) != 0 {
		t.Fatalf("flush request=%#v", flushRequest)
	}

	secondStore, err := memory.Open(root, workspaceRoot, "second-session")
	if err != nil {
		t.Fatal(err)
	}
	secondStreamer := &fakeStreamer{results: []api.StreamResult{{ResponseID: "fresh", Text: "done"}}}
	second := Runner{
		Client: secondStreamer, Tools: tools.NewRegistry(ws, tools.PromptApprover{Mode: tools.PermissionAuto}),
		Model: "test", Memory: secondStore, MemoryConfig: config,
	}
	defer second.Tools.Close()
	if _, err := second.RunTurn(context.Background(), "new task", ""); err != nil {
		t.Fatal(err)
	}
	content, _ := secondStreamer.requests[0].Input[0].Content.(string)
	if !strings.Contains(content, "<memory-context>") || !strings.Contains(content, "Keep the memory boundary small") || !strings.HasSuffix(content, "new task") {
		t.Fatalf("memory was not injected into the next session: %q", content)
	}
	resumeStreamer := &fakeStreamer{results: []api.StreamResult{{ResponseID: "resumed", Text: "done"}}}
	resumed := Runner{
		Client: resumeStreamer, Tools: tools.NewRegistry(ws, tools.PromptApprover{Mode: tools.PermissionAuto}),
		Model: "test", Memory: secondStore, MemoryConfig: config,
	}
	defer resumed.Tools.Close()
	if _, err := resumed.RunTurn(context.Background(), "resume task", "existing-response"); err != nil {
		t.Fatal(err)
	}
	resumedContent, _ := resumeStreamer.requests[0].Input[0].Content.(string)
	if strings.Contains(resumedContent, "<memory-context>") {
		t.Fatalf("resumed response chain duplicated memory context: %q", resumedContent)
	}
}

func TestProcessMemoryFlushResponseQualityControls(t *testing.T) {
	for _, value := range []string{"", "NO_REPLY", "no reply", "No-Reply", "noreply"} {
		if content, outcome := processMemoryFlushResponse(value, 8000); content != "" || outcome != "nothing_to_store" {
			t.Fatalf("value=%q content=%q outcome=%q", value, content, outcome)
		}
	}
	if content, outcome := processMemoryFlushResponse("plain text", 8000); content != "" || outcome != "rejected" {
		t.Fatalf("content=%q outcome=%q", content, outcome)
	}
	content, outcome := processMemoryFlushResponse("## Context\n"+strings.Repeat("界", 100), 20)
	if outcome != "accepted" || len([]rune(content)) != 20 {
		t.Fatalf("content=%q chars=%d outcome=%q", content, len([]rune(content)), outcome)
	}
}

func TestRunnerManualMemoryFlushUsesSharedLifecycle(t *testing.T) {
	root, workspaceRoot := t.TempDir(), t.TempDir()
	store, err := memory.Open(root, workspaceRoot, "manual")
	if err != nil {
		t.Fatal(err)
	}
	config := memory.DefaultConfig()
	config.Enabled = true
	streamer := &fakeStreamer{results: []api.StreamResult{
		{ResponseID: "flush-one", Text: "## Decision\n\nUse explicit flush."},
		{ResponseID: "flush-two", Text: "NO_REPLY"},
	}}
	runner := Runner{Client: streamer, Model: "test", Memory: store, MemoryConfig: config}
	result, err := runner.FlushMemory(context.Background(), "current")
	if err != nil || result.Outcome != "written" || result.Path == "" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	result, err = runner.FlushMemory(context.Background(), "current")
	if err != nil || result.Outcome != "nothing_to_store" {
		t.Fatalf("delta result=%#v err=%v", result, err)
	}
	if len(streamer.requests) != 2 || streamer.requests[0].PreviousResponseID != "current" || !strings.Contains(streamer.requests[1].Instructions, "previous flush") {
		t.Fatalf("requests=%#v", streamer.requests)
	}
	runner.memoryFlushRunning = true
	if _, err := runner.FlushMemory(context.Background(), "current"); err == nil || !strings.Contains(err.Error(), "already in progress") {
		t.Fatalf("concurrent err=%v", err)
	}
	runner.memoryFlushRunning = false
	if _, err := runner.FlushMemory(context.Background(), ""); err == nil || !strings.Contains(err.Error(), "no completed response") {
		t.Fatalf("empty history err=%v", err)
	}
}

func TestRunnerRewritesMemoryNoteWithIsolatedBoundedRequest(t *testing.T) {
	streamer := &memoryRewriteStreamer{result: api.StreamResult{Text: "## Deployment\n\n- Run the release checks."}}
	runner := Runner{Client: streamer, Model: "session-model"}
	rewritten, err := runner.RewriteMemoryNote(context.Background(), "run release checks", "deployment workflow")
	if err != nil || rewritten != streamer.result.Text {
		t.Fatalf("rewritten=%q err=%v", rewritten, err)
	}
	request := streamer.request
	input, _ := request.Input[0].Content.(string)
	if streamer.includeHistory == nil || *streamer.includeHistory || request.Model != "grok-build" || request.MaxOutputTokens != 1024 || request.Temperature == nil || *request.Temperature != 0.3 || request.PreviousResponseID != "" || len(request.Tools) != 0 || !strings.Contains(request.Instructions, "memory note formatter") || !strings.Contains(input, "deployment workflow") || !strings.HasSuffix(input, "run release checks") {
		t.Fatalf("includeHistory=%v request=%#v", streamer.includeHistory, request)
	}
	before := streamer.request
	if _, err := runner.RewriteMemoryNote(context.Background(), strings.Repeat("x", (32<<10)+1), ""); err == nil || !strings.Contains(err.Error(), "input too large") {
		t.Fatalf("oversize err=%v", err)
	}
	if streamer.request.Input[0].Content != before.Input[0].Content {
		t.Fatal("oversize rewrite reached the model")
	}
	streamer.result.Text = ""
	if _, err := runner.RewriteMemoryNote(context.Background(), "note", "context"); err == nil || !strings.Contains(err.Error(), "empty response") {
		t.Fatalf("empty err=%v", err)
	}
}

func TestRunnerEnhancesAndSavesGlobalMemoryWhileRetrievalDisabled(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	logger, err := session.NewLoggerWithID(t.TempDir(), "remember")
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	if err := logger.Append("session_metadata", map[string]any{"cwd": "/workspace/project", "headCommit": "abc123"}); err != nil {
		t.Fatal(err)
	}
	if err := logger.AppendPrompt("deploy through the release pipeline", nil); err != nil {
		t.Fatal(err)
	}
	streamer := &memoryRewriteStreamer{result: api.StreamResult{Text: "## Deployment\n\n- Run release checks."}}
	runner := Runner{Client: streamer, SessionPath: logger.Path(), MemoryConfig: memory.DefaultConfig()}
	if enhanced := runner.EnhanceMemoryNote(context.Background(), "run checks"); enhanced != streamer.result.Text {
		t.Fatalf("enhanced=%q", enhanced)
	}
	input, _ := streamer.request.Input[0].Content.(string)
	if !strings.Contains(input, "deploy through the release pipeline") || !strings.Contains(input, "/workspace/project") || !strings.Contains(input, "abc123") {
		t.Fatalf("rewrite input=%q", input)
	}
	path, err := runner.SaveMemoryNote("always open the pull request")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "## always open the pull request" || runner.Memory != nil || runner.MemoryConfig.Enabled {
		t.Fatalf("data=%q config=%#v err=%v", data, runner.MemoryConfig, err)
	}
	streamer.err = errors.New("offline")
	if enhanced := runner.EnhanceMemoryNote(context.Background(), "raw fallback"); enhanced != "" {
		t.Fatalf("failed rewrite=%q", enhanced)
	}
}

func TestRunnerDreamUsesIsolatedModelAndCommitsWorkspaceMemory(t *testing.T) {
	root, workspaceRoot := t.TempDir(), t.TempDir()
	prior, err := memory.Open(root, workspaceRoot, "prior")
	if err != nil {
		t.Fatal(err)
	}
	path, _, err := prior.Write("session_end", "## Decision\n\nUse the release pipeline.")
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-10 * time.Minute)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	store, err := memory.Open(root, workspaceRoot, "current")
	if err != nil {
		t.Fatal(err)
	}
	cfg := memory.DefaultConfig()
	cfg.Enabled = true
	streamer := &memoryRewriteStreamer{result: api.StreamResult{Text: "## Deployment\n\nUse the release pipeline."}}
	runner := Runner{Client: streamer, Model: "session-model", Memory: store, MemoryConfig: cfg}
	result, err := runner.DreamMemory(context.Background(), true)
	if err != nil || result.Outcome != "written" || result.Cleaned != 1 || result.Path == "" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	request := streamer.request
	if streamer.includeHistory == nil || *streamer.includeHistory || request.Model != "session-model" || request.PreviousResponseID != "" || len(request.Tools) != 0 || !strings.Contains(request.Instructions, "reflective pass") {
		t.Fatalf("request=%#v includeHistory=%v", request, streamer.includeHistory)
	}
	if data, err := os.ReadFile(result.Path); err != nil || string(data) != streamer.result.Text {
		t.Fatalf("memory=%q err=%v", data, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("processed session survived: %v", err)
	}
}

func TestRunnerAutoDreamRunsAtCloseAndConfiguredIdleInterval(t *testing.T) {
	for _, mode := range []string{"close", "interval"} {
		t.Run(mode, func(t *testing.T) {
			root, workspaceRoot := t.TempDir(), t.TempDir()
			prior, err := memory.Open(root, workspaceRoot, "prior")
			if err != nil {
				t.Fatal(err)
			}
			path, _, err := prior.Write("session_end", "## Decision\n\nKeep durable context.")
			if err != nil {
				t.Fatal(err)
			}
			old := time.Now().Add(-10 * time.Minute)
			if err := os.Chtimes(path, old, old); err != nil {
				t.Fatal(err)
			}
			store, err := memory.Open(root, workspaceRoot, "current")
			if err != nil {
				t.Fatal(err)
			}
			cfg := memory.DefaultConfig()
			cfg.Enabled, cfg.Dream.MinHours, cfg.Dream.MinSessions = true, 0, 1
			streamer := &memoryRewriteStreamer{result: api.StreamResult{Text: "## Durable\n\nConsolidated context."}}
			runner := Runner{Client: streamer, Model: "test", Memory: store, MemoryConfig: cfg}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if mode == "close" {
				if err := runner.CloseMemory(ctx); err != nil {
					t.Fatal(err)
				}
			} else {
				seconds := uint64(1)
				runner.MemoryConfig.Dream.CheckIntervalSeconds = &seconds
				runner.scheduleMemoryDreamCheck(ctx)
				for {
					files, err := store.List()
					found := false
					for _, file := range files {
						found = found || file.Source == "workspace"
					}
					if err == nil && found {
						break
					}
					select {
					case <-ctx.Done():
						t.Fatal("periodic dream did not run")
					case <-time.After(20 * time.Millisecond):
					}
				}
				if err := runner.WaitMemory(ctx); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("session survived %s dream: %v", mode, err)
			}
		})
	}
}

func TestRunnerIdleMemoryFlushTriggersAndStopsWithSession(t *testing.T) {
	root, workspaceRoot := t.TempDir(), t.TempDir()
	store, err := memory.Open(root, workspaceRoot, "idle")
	if err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Open(workspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	zero := uint64(0)
	config := memory.DefaultConfig()
	config.Enabled = true
	config.Flush.IdleTimeoutSeconds = &zero
	streamer := &memoryFlushStreamer{flushDone: make(chan struct{})}
	runner := Runner{
		Client: streamer, Tools: tools.NewRegistry(ws, tools.PromptApprover{Mode: tools.PermissionAuto}),
		Model: "test", Memory: store, MemoryConfig: config,
	}
	defer runner.Tools.Close()
	if _, err := runner.RunTurn(context.Background(), "remember after idle", ""); err != nil {
		t.Fatal(err)
	}
	select {
	case <-streamer.flushDone:
	case <-time.After(time.Second):
		t.Fatal("idle memory flush did not start")
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := runner.WaitMemory(waitCtx); err != nil {
		t.Fatal(err)
	}
	requests := streamer.snapshot()
	if len(requests) != 2 || requests[1].PreviousResponseID != "old" || !strings.Contains(requests[1].Instructions, "memory assistant") {
		t.Fatalf("requests=%#v", requests)
	}
	value, err := store.Context()
	if err != nil || !strings.Contains(value, "interval-idle") {
		t.Fatalf("memory=%q err=%v", value, err)
	}

	long := uint64(60)
	config.Flush.IdleTimeoutSeconds = &long
	stoppedStreamer := &memoryFlushStreamer{flushDone: make(chan struct{})}
	stopped := Runner{
		Client: stoppedStreamer, Tools: tools.NewRegistry(ws, tools.PromptApprover{Mode: tools.PermissionAuto}),
		Model: "test", Memory: store, MemoryConfig: config,
	}
	defer stopped.Tools.Close()
	if _, err := stopped.RunTurn(context.Background(), "do not wait a minute", ""); err != nil {
		t.Fatal(err)
	}
	stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
	defer stopCancel()
	if err := stopped.WaitMemory(stopCtx); err != nil {
		t.Fatal(err)
	}
	if requests := stoppedStreamer.snapshot(); len(requests) != 1 {
		t.Fatalf("shutdown allowed pending idle flush: %#v", requests)
	}
}

func TestRunnerSavesQualifiedSessionMemoryOnClose(t *testing.T) {
	root, workspaceRoot := t.TempDir(), t.TempDir()
	store, err := memory.Open(root, workspaceRoot, "session-end")
	if err != nil {
		t.Fatal(err)
	}
	logger, err := session.NewLoggerWithID(t.TempDir(), "session-end")
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	for _, event := range []struct {
		kind string
		data map[string]any
	}{
		{"user_prompt", map[string]any{"text": "internal continuation", "synthetic": true}},
		{"user_prompt", map[string]any{"text": "Investigate authentication callback failures"}},
		{"model_response", map[string]any{"response_id": "one", "text": "checked", "tool_call_count": 0}},
		{"user_prompt", map[string]any{"text": "Preserve the existing API compatibility tests"}},
		{"model_response", map[string]any{"response_id": "two", "text": "preserved", "tool_call_count": 0}},
		{"tool_result", map[string]any{"name": "bash", "output": "ok"}},
	} {
		if err := logger.Append(event.kind, event.data); err != nil {
			t.Fatal(err)
		}
	}
	ws, err := workspace.Open(workspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	config := memory.DefaultConfig()
	config.Enabled = true
	runner := Runner{
		Client: &fakeStreamer{results: []api.StreamResult{{ResponseID: "three", Text: "done"}}},
		Tools:  tools.NewRegistry(ws, tools.PromptApprover{Mode: tools.PermissionAuto}), Logger: logger,
		Model: "test", Memory: store, MemoryConfig: config, SessionPath: logger.Path(),
	}
	defer runner.Tools.Close()
	if _, err := runner.RunTurn(context.Background(), "Document the final deployment verification", "two"); err != nil {
		t.Fatal(err)
	}
	closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := runner.CloseMemory(closeCtx); err != nil {
		t.Fatal(err)
	}
	if err := runner.CloseMemory(closeCtx); err != nil {
		t.Fatal(err)
	}
	value, err := store.Context()
	if err != nil || !strings.Contains(value, "3 user, 3 assistant, 1 tool results") || !strings.Contains(value, "Investigate authentication callback failures") || strings.Contains(value, "internal continuation") || strings.Count(value, "## Session Summary") != 1 {
		t.Fatalf("memory=%q err=%v", value, err)
	}
}

func TestRunnerSkipsSessionMemoryWhenDisabledOrTooShort(t *testing.T) {
	for _, test := range []struct {
		name      string
		saveOnEnd bool
		prompts   []string
	}{
		{name: "disabled", saveOnEnd: false, prompts: []string{"A sufficiently long first prompt", "A sufficiently long second prompt", "A sufficiently long third prompt"}},
		{name: "too short", saveOnEnd: true, prompts: []string{"one", "two", "three"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, err := memory.Open(t.TempDir(), t.TempDir(), test.name)
			if err != nil {
				t.Fatal(err)
			}
			logger, err := session.NewLoggerWithID(t.TempDir(), "skip-session")
			if err != nil {
				t.Fatal(err)
			}
			defer logger.Close()
			for _, prompt := range test.prompts {
				if err := logger.Append("user_prompt", map[string]any{"text": prompt}); err != nil {
					t.Fatal(err)
				}
			}
			config := memory.DefaultConfig()
			config.Enabled, config.SaveOnEnd = true, test.saveOnEnd
			runner := Runner{Memory: store, MemoryConfig: config, SessionPath: logger.Path()}
			if err := runner.CloseMemory(context.Background()); err != nil {
				t.Fatal(err)
			}
			if value, err := store.Context(); err != nil || value != "" {
				t.Fatalf("memory=%q err=%v", value, err)
			}
		})
	}
}
