package agent

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lookcorner/go-cli/internal/api"
	"github.com/lookcorner/go-cli/internal/memory"
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
