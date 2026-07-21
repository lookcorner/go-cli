package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/api"
	"github.com/lookcorner/go-cli/internal/tools"
	"github.com/lookcorner/go-cli/internal/workspace"
)

type scheduledStreamer struct {
	mu       sync.Mutex
	results  []api.StreamResult
	requests []api.ResponseRequest
	called   chan struct{}
}

func (s *scheduledStreamer) StreamResponse(_ context.Context, request api.ResponseRequest, _ func(string)) (api.StreamResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, request)
	if s.called != nil {
		select {
		case s.called <- struct{}{}:
		default:
		}
	}
	result := s.results[0]
	s.results = s.results[1:]
	return result, nil
}

func TestScheduledWakeQueueTracksTasksAndDeduplicates(t *testing.T) {
	queue := newScheduledWakeQueue()
	created := tools.ScheduledTaskCreated{TaskID: "loop-1", Prompt: "check", HumanSchedule: "every minute"}
	queue.ScheduledTaskCreated(created)
	queue.ScheduledTaskFired(created)
	queue.ScheduledTaskFired(created)
	event, ok := queue.Take()
	if !ok || event.TaskID != "loop-1" || event.HumanSchedule != "every minute" || !queue.ShouldWait() {
		t.Fatalf("event=%#v ok=%v wait=%v", event, ok, queue.ShouldWait())
	}
	if _, duplicate := queue.Take(); duplicate {
		t.Fatal("duplicate fire was queued")
	}
	queue.ScheduledTaskFired(created)
	if _, duplicate := queue.Take(); duplicate {
		t.Fatal("active task was queued again")
	}
	queue.Done("loop-1")
	if !queue.ShouldWait() {
		t.Fatal("recurring scheduler task stopped being tracked after firing")
	}
	queue.ScheduledTaskRemoved("loop-1")
	if queue.ShouldWait() {
		t.Fatal("removed and completed task kept queue alive")
	}
}

func TestLocalWakeQueueTracksQueuesAndCancelsBackgroundWork(t *testing.T) {
	queue := newScheduledWakeQueue()
	queue.TrackWake("task-1")
	if !queue.ShouldWait() {
		t.Fatal("running task did not keep headless queue alive")
	}
	if !queue.QueueWake("task-1", "task finished") || queue.QueueWake("task-1", "duplicate") {
		t.Fatal("completion wake was not queued exactly once")
	}
	queue.CancelWake("task-1")
	if queue.ShouldWait() {
		t.Fatal("cancelled completion remained queued")
	}
	queue.TrackWake("task-2")
	if !queue.QueueWake("task-2", "second finished") {
		t.Fatal("second completion was not queued")
	}
	event, ok := queue.Take()
	if !ok || event.TaskID != "task-2" || event.Prompt != "second finished" {
		t.Fatalf("event=%#v ok=%v", event, ok)
	}
	queue.Done(event.TaskID)
	if queue.ShouldWait() {
		t.Fatal("completed wake kept queue alive")
	}
}

func TestRunHeadlessDrainsScheduledWakesInResponseChain(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry(ws, tools.PromptApprover{Mode: tools.PermissionAuto})
	defer registry.Close()
	streamer := &scheduledStreamer{results: []api.StreamResult{{ResponseID: "user-response", Text: "user"}, {ResponseID: "scheduled-response", Text: "scheduled"}}}
	runner := &agent.Runner{Client: streamer, Tools: registry, Model: "test"}
	queue := newScheduledWakeQueue()
	event := tools.ScheduledTaskFired{TaskID: "loop-1", Prompt: "check deployment"}
	queue.ScheduledTaskCreated(event)
	queue.ScheduledTaskFired(event)
	queue.ScheduledTaskRemoved(event.TaskID)
	var stdout, stderr bytes.Buffer
	if err := runHeadless(context.Background(), runner, queue, &stdout, &stderr, "user prompt", "parent-response"); err != nil {
		t.Fatal(err)
	}
	streamer.mu.Lock()
	defer streamer.mu.Unlock()
	if len(streamer.requests) != 2 || streamer.requests[0].PreviousResponseID != "parent-response" || streamer.requests[1].PreviousResponseID != "user-response" {
		t.Fatalf("requests=%#v", streamer.requests)
	}
	input, _ := json.Marshal(streamer.requests[1].Input)
	if !bytes.Contains(input, []byte("check deployment")) || !bytes.Contains(stderr.Bytes(), []byte("loop-1")) || queue.ShouldWait() {
		t.Fatalf("input=%s stderr=%q wait=%v", input, stderr.String(), queue.ShouldWait())
	}
}

func TestRunHeadlessExecutesSchedulerCreateFireImmediately(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry(ws, tools.PromptApprover{Mode: tools.PermissionAuto})
	defer registry.Close()
	queue := newScheduledWakeQueue()
	registry.SetSchedulerObserver(queue)
	streamer := &scheduledStreamer{results: []api.StreamResult{
		{ResponseID: "tool-response", ToolCalls: []api.ToolCall{{CallID: "call-1", Name: "scheduler_create", Arguments: json.RawMessage(`{"interval":"1m","prompt":"check deploy","recurring":false,"fire_immediately":true}`)}}},
		{ResponseID: "user-response", Text: "created"},
		{ResponseID: "scheduled-response", Text: "checked"},
	}}
	runner := &agent.Runner{Client: streamer, Tools: registry, Model: "test"}
	if err := runHeadless(context.Background(), runner, queue, io.Discard, io.Discard, "create a loop", "parent-response"); err != nil {
		t.Fatal(err)
	}
	streamer.mu.Lock()
	defer streamer.mu.Unlock()
	if len(streamer.requests) != 3 || streamer.requests[2].PreviousResponseID != "user-response" {
		t.Fatalf("requests=%#v", streamer.requests)
	}
	input, _ := json.Marshal(streamer.requests[2].Input)
	if !bytes.Contains(input, []byte("check deploy")) || queue.ShouldWait() {
		t.Fatalf("input=%s wait=%v", input, queue.ShouldWait())
	}
}

func TestRunHeadlessAutoWakesForBackgroundProcessCompletion(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry(ws, tools.PromptApprover{Mode: tools.PermissionAuto})
	defer registry.Close()
	queue := newScheduledWakeQueue()
	registry.SetProcessObserver(&sessionProcessObserver{autoWake: true, wake: queue})
	streamer := &scheduledStreamer{results: []api.StreamResult{
		{ResponseID: "tool-response", ToolCalls: []api.ToolCall{{CallID: "call-1", Name: "run_terminal_cmd", Arguments: json.RawMessage(`{"command":"sleep 0.05; printf done","description":"finish later","is_background":true}`)}}},
		{ResponseID: "user-response", Text: "backgrounded"},
		{ResponseID: "wake-response", Text: "handled"},
	}}
	runner := &agent.Runner{Client: streamer, Tools: registry, Model: "test"}
	if err := runHeadless(context.Background(), runner, queue, io.Discard, io.Discard, "start work", ""); err != nil {
		t.Fatal(err)
	}
	streamer.mu.Lock()
	defer streamer.mu.Unlock()
	if len(streamer.requests) != 3 || streamer.requests[2].PreviousResponseID != "user-response" {
		t.Fatalf("requests=%#v", streamer.requests)
	}
	input, _ := json.Marshal(streamer.requests[2].Input)
	if !bytes.Contains(input, []byte("Background task")) || !bytes.Contains(input, []byte("get_task_output")) || queue.ShouldWait() {
		t.Fatalf("input=%s wait=%v", input, queue.ShouldWait())
	}
}

func TestInteractiveRunsScheduleWhileWaitingForInput(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry(ws, tools.PromptApprover{Mode: tools.PermissionAuto})
	defer registry.Close()
	streamer := &scheduledStreamer{results: []api.StreamResult{{ResponseID: "scheduled-response", Text: "scheduled"}}, called: make(chan struct{}, 1)}
	runner := &agent.Runner{Client: streamer, Tools: registry, Model: "test"}
	queue := newScheduledWakeQueue()
	event := tools.ScheduledTaskFired{TaskID: "loop-1", Prompt: "scheduled prompt"}
	queue.ScheduledTaskCreated(event)
	queue.ScheduledTaskFired(event)
	queue.ScheduledTaskRemoved(event.TaskID)
	reader, writer := io.Pipe()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- interactiveLoop(ctx, runner, queue, newTerminalInput(ctx, bufio.NewReader(reader)), io.Discard, io.Discard, "", "parent-response")
	}()
	select {
	case <-streamer.called:
	case <-ctx.Done():
		t.Fatal("scheduled prompt did not run while input was idle")
	}
	_ = writer.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	streamer.mu.Lock()
	defer streamer.mu.Unlock()
	if len(streamer.requests) != 1 || streamer.requests[0].PreviousResponseID != "parent-response" {
		t.Fatalf("requests=%#v", streamer.requests)
	}
}
