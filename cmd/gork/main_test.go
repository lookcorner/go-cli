package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/api"
	"github.com/lookcorner/go-cli/internal/auth"
	"github.com/lookcorner/go-cli/internal/compat"
	"github.com/lookcorner/go-cli/internal/config"
	"github.com/lookcorner/go-cli/internal/marketplace"
	"github.com/lookcorner/go-cli/internal/mcp"
	"github.com/lookcorner/go-cli/internal/memory"
	"github.com/lookcorner/go-cli/internal/plugin"
	"github.com/lookcorner/go-cli/internal/session"
	"github.com/lookcorner/go-cli/internal/subagent"
	"github.com/lookcorner/go-cli/internal/tools"
	"github.com/lookcorner/go-cli/internal/version"
	"github.com/lookcorner/go-cli/internal/workspace"
)

type samplingStreamer struct {
	request api.ResponseRequest
}

type memoryCommandStreamer struct{ request api.ResponseRequest }

func (s *memoryCommandStreamer) StreamResponse(_ context.Context, request api.ResponseRequest, _ func(string)) (api.StreamResult, error) {
	s.request = request
	return api.StreamResult{Text: "## Decision\n\nFlush explicitly."}, nil
}

type failingGoalStreamer struct{ err error }

type interactiveStatusStreamer struct{ calls int }

func TestSessionMetadataWithDisplayCWD(t *testing.T) {
	root := t.TempDir()
	metadata := sessionMetadataWithDisplay(context.Background(), root, "model", "high", "  /project  ")
	if metadata["cwd"] != root || metadata["displayCwd"] != "/project" || metadata["modelId"] != "model" || metadata["reasoningEffort"] != "high" {
		t.Fatalf("metadata=%#v", metadata)
	}
}

func TestFeedbackSubmitterAddsSessionMetadata(t *testing.T) {
	logger, err := session.NewLoggerWithID(t.TempDir(), "local-feedback")
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	submit := feedbackSubmitter(logger, "grok-build", "grok-4", "/workspace")
	if err := submit(session.UserFeedback{Text: "keep local", ClientType: "tui"}); err != nil {
		t.Fatal(err)
	}
	events, err := session.Events(logger.Path(), "user_feedback")
	if err != nil || len(events) != 1 {
		t.Fatalf("events=%#v err=%v", events, err)
	}
	data := events[0].Data.(map[string]any)
	if data["sessionId"] != logger.ID() || data["modelId"] != "grok-build" || data["resolvedModelId"] != "grok-4" || data["clientVersion"] != version.Current || data["cwd"] != "/workspace" || data["text"] != "keep local" {
		t.Fatalf("feedback=%#v", data)
	}
	if err := submit(session.UserFeedback{Text: "current", ModelID: "selected", ResolvedModelID: "selected-api"}); err != nil {
		t.Fatal(err)
	}
	events, err = session.Events(logger.Path(), "user_feedback")
	if err != nil || len(events) != 2 {
		t.Fatalf("events=%#v err=%v", events, err)
	}
	data = events[1].Data.(map[string]any)
	if data["modelId"] != "selected" || data["resolvedModelId"] != "selected-api" {
		t.Fatalf("current model metadata=%#v", data)
	}
}

func (s failingGoalStreamer) StreamResponse(context.Context, api.ResponseRequest, func(string)) (api.StreamResult, error) {
	return api.StreamResult{}, s.err
}

func (s *interactiveStatusStreamer) StreamResponse(_ context.Context, _ api.ResponseRequest, stream func(string)) (api.StreamResult, error) {
	s.calls++
	stream("done")
	return api.StreamResult{ResponseID: "response-1", Text: "done", Usage: api.Usage{InputTokens: 250}}, nil
}

func TestInteractiveSessionInfoAliasesAndContext(t *testing.T) {
	root := t.TempDir()
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry(ws, tools.PromptApprover{Mode: tools.PermissionAuto})
	defer registry.Close()
	logger, err := session.NewLoggerWithID(t.TempDir(), "interactive-status")
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	streamer := &interactiveStatusStreamer{}
	runner := &agent.Runner{
		Client: streamer, Tools: registry, Logger: logger, SessionID: logger.ID(), SessionPath: logger.Path(),
		Workspace: root, ModelID: "grok-build", Model: "model-id", ContextWindow: 1000, MaxSteps: 1,
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	input := newTerminalInput(ctx, bufio.NewReader(strings.NewReader("/status before\n/context before\nhello\n/info extra\n/context now\n/exit\n")))
	var stderr bytes.Buffer
	if err := interactiveLoop(ctx, runner, newScheduledWakeQueue(), input, io.Discard, &stderr, "", ""); err != nil {
		t.Fatal(err)
	}
	output := stderr.String()
	if streamer.calls != 1 || strings.Count(output, "[gork] session: interactive-status") != 2 || !strings.Contains(output, "[gork] workspace: "+root) || !strings.Contains(output, "[gork] model: grok-build") || !strings.Contains(output, "[gork] turn: 0") || !strings.Contains(output, "[gork] turn: 1") || strings.Count(output, "[gork] context: 0 / 1000 tokens (0%)") != 2 || strings.Count(output, "[gork] context: 250 / 1000 tokens (25%)") != 2 {
		t.Fatalf("calls=%d output=%q", streamer.calls, output)
	}
}

func TestInteractivePrivacyCommandsDoNotRunModelTurn(t *testing.T) {
	streamer := &interactiveStatusStreamer{}
	runner := &agent.Runner{Client: streamer, Model: "test"}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	input := newTerminalInput(ctx, bufio.NewReader(strings.NewReader("/privacy\n/privacy private\n/privacy opt-in\n/privacy on\n/help\n/exit\n")))
	var stderr bytes.Buffer
	if err := interactiveLoop(ctx, runner, newScheduledWakeQueue(), input, io.Discard, &stderr, "", ""); err != nil {
		t.Fatal(err)
	}
	output := stderr.String()
	for _, want := range []string{"Product: Gork Build", "Coding data sharing: Opt out", agent.PrivacyLockedMessage, "Unknown argument", "/privacy [opt-out]"} {
		if !strings.Contains(output, want) {
			t.Errorf("missing %q in %q", want, output)
		}
	}
	if streamer.calls != 0 {
		t.Fatalf("model calls=%d output=%q", streamer.calls, output)
	}
}

func TestInteractiveTerminalSetupCommandsDoNotRunModelTurn(t *testing.T) {
	streamer := &interactiveStatusStreamer{}
	runner := &agent.Runner{Client: streamer, Model: "test"}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	input := newTerminalInput(ctx, bufio.NewReader(strings.NewReader("/terminal-setup\n/terminal-check ignored\n/terminal-info\n/help\n/exit\n")))
	var stderr bytes.Buffer
	if err := interactiveLoop(ctx, runner, newScheduledWakeQueue(), input, io.Discard, &stderr, "", ""); err != nil {
		t.Fatal(err)
	}
	output := stderr.String()
	if streamer.calls != 0 || strings.Count(output, "Environment\n") != 3 || strings.Count(output, "Clipboard routes") != 3 || !strings.Contains(output, "/terminal-setup") {
		t.Fatalf("model calls=%d output=%q", streamer.calls, output)
	}
}

func TestInteractiveUsageCommandsDoNotRunModelTurn(t *testing.T) {
	streamer := &interactiveStatusStreamer{}
	fetches, opened := 0, ""
	runner := &agent.Runner{
		Client: streamer, Model: "test",
		FetchUsage: func(context.Context) (string, error) {
			fetches++
			return "Weekly limit: 42%", nil
		},
		OpenURL: func(url string) bool { opened = url; return false },
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	input := newTerminalInput(ctx, bufio.NewReader(strings.NewReader("/usage\n/cost show\n/usage manage\n/usage BAD\n/help\n/exit\n")))
	var stderr bytes.Buffer
	if err := interactiveLoop(ctx, runner, newScheduledWakeQueue(), input, io.Discard, &stderr, "", ""); err != nil {
		t.Fatal(err)
	}
	output := stderr.String()
	for _, want := range []string{"Weekly limit: 42%", "https://grok.com/?_s=usage", "Unknown argument: BAD", "/usage [show|manage]"} {
		if !strings.Contains(output, want) {
			t.Errorf("missing %q in %q", want, output)
		}
	}
	if streamer.calls != 0 || fetches != 2 || opened != "https://grok.com/?_s=usage" {
		t.Fatalf("model calls=%d fetches=%d opened=%q output=%q", streamer.calls, fetches, opened, output)
	}
}

func TestInteractiveRememberModeTreatsPrivacyAsNoteText(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GROK_HOME", root)
	runner := &agent.Runner{}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	input := newTerminalInput(ctx, bufio.NewReader(strings.NewReader("/remember\n/privacy\ny\n/exit\n")))
	var stderr bytes.Buffer
	if err := interactiveLoop(ctx, runner, newScheduledWakeQueue(), input, io.Discard, &stderr, "", ""); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(memory.GlobalPath(filepath.Join(root, "memory")))
	if err != nil || !strings.Contains(string(content), "/privacy") || strings.Contains(stderr.String(), "Product: Gork Build") {
		t.Fatalf("content=%q err=%v output=%q", content, err, stderr.String())
	}
}

func TestNewPermissionClassifierConfig(t *testing.T) {
	cfg := config.Config{Model: "main", Backend: "responses", BaseURL: "https://main.example/v1"}
	inherited, err := newPermissionClassifierConfig(cfg, nil)
	if err != nil || inherited.Client != nil || inherited.Model != "" || inherited.PromptType != "full" {
		t.Fatalf("inherited=%#v err=%v", inherited, err)
	}
	cfg.AutoMode = config.AutoModeConfig{ClassifierModel: "classifier", PromptType: "just_command", ReasoningEffort: "high"}
	cfg.ModelProfiles = map[string]config.ModelProfile{
		"classifier": {Model: "classifier-id", Backend: "responses", BaseURL: "https://classifier.example/v1"},
	}
	dedicated, err := newPermissionClassifierConfig(cfg, nil)
	if err != nil || dedicated.Client == nil || dedicated.Model != "classifier-id" || dedicated.PromptType != "just_command" || dedicated.ReasoningEffort != "high" {
		t.Fatalf("dedicated=%#v err=%v", dedicated, err)
	}
	cfg.AutoMode.ClassifierModel = "missing"
	if _, err := newPermissionClassifierConfig(cfg, nil); err == nil || !strings.Contains(err.Error(), "is not defined") {
		t.Fatalf("missing classifier error=%v", err)
	}
}

func TestInteractiveMemoryFlushDoesNotRunNormalTurn(t *testing.T) {
	store, err := memory.Open(t.TempDir(), t.TempDir(), "interactive")
	if err != nil {
		t.Fatal(err)
	}
	config := memory.DefaultConfig()
	config.Enabled = true
	streamer := &memoryCommandStreamer{}
	runner := &agent.Runner{Client: streamer, Model: "test", Memory: store, MemoryConfig: config}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	input := newTerminalInput(ctx, bufio.NewReader(strings.NewReader("/flush\n/exit\n")))
	var stderr bytes.Buffer
	if err := interactiveLoop(ctx, runner, newScheduledWakeQueue(), input, io.Discard, &stderr, "", "response-1"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(streamer.request.Instructions, "memory assistant") || !strings.Contains(stderr.String(), "memory flush: written") {
		t.Fatalf("request=%#v stderr=%q", streamer.request, stderr.String())
	}
}

func TestInteractiveShellDoesNotRunNormalTurn(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry(ws, tools.PromptApprover{Mode: tools.PermissionAuto})
	defer registry.Close()
	runner := &agent.Runner{Tools: registry}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	input := newTerminalInput(ctx, bufio.NewReader(strings.NewReader("! printf interactive-shell\n/exit\n")))
	var stdout, stderr bytes.Buffer
	if err := interactiveLoop(ctx, runner, newScheduledWakeQueue(), input, &stdout, &stderr, "", "response-1"); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "interactive-shell\n" || strings.Contains(stderr.String(), "turn failed") {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestMemoryClearCommandScopesConfirmationAndValidation(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	t.Setenv("GROK_HOME", home)
	root := filepath.Join(home, "memory")
	store, err := memory.Open(root, cwd, "clear-command")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Write("manual", "workspace note"); err != nil {
		t.Fatal(err)
	}
	global, err := memory.AppendGlobal(root, "global note")
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if err := runMemory([]string{"clear"}, cwd, strings.NewReader("n\n"), &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	workspacePath, _ := memory.WorkspacePath(root, cwd)
	if !strings.Contains(stdout.String(), "Cancelled.") {
		t.Fatalf("stdout=%q", stdout.String())
	}
	if _, err := os.Stat(workspacePath); err != nil {
		t.Fatalf("cancel removed workspace memory: %v", err)
	}
	stdout.Reset()
	if err := runMemory([]string{"clear", "--all", "-y"}, cwd, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if strings.Count(stdout.String(), "Cleared:") != 2 || !strings.Contains(stdout.String(), "Memory cleared.") {
		t.Fatalf("stdout=%q", stdout.String())
	}
	for _, path := range []string{workspacePath, global} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("path still exists %s: %v", path, err)
		}
	}
	stdout.Reset()
	if err := runMemory([]string{"clear", "--global", "--yes"}, cwd, strings.NewReader(""), &stdout, &stderr); err != nil || !strings.Contains(stdout.String(), "Nothing to clear") {
		t.Fatalf("stdout=%q err=%v", stdout.String(), err)
	}
	if err := runMemory([]string{"clear", "--workspace", "--global"}, cwd, strings.NewReader(""), io.Discard, io.Discard); err == nil {
		t.Fatal("conflicting scopes were accepted")
	}

	store, err = memory.Open(root, cwd, "partial-clear")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Write("manual", "another workspace note"); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(outside, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, memory.GlobalPath(root)); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if err := runMemory([]string{"clear", "--all", "--yes"}, cwd, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "Memory partially cleared. Errors:") || !strings.Contains(stderr.String(), "global MEMORY.md") {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if data, err := os.ReadFile(outside); err != nil || string(data) != "keep" {
		t.Fatalf("outside=%q err=%v", data, err)
	}
	stdout.Reset()
	stderr.Reset()
	if err := runMemory([]string{"clear", "--global", "--yes"}, cwd, strings.NewReader(""), &stdout, &stderr); err == nil {
		t.Fatal("failed-only clear returned success")
	}
	if !strings.Contains(stderr.String(), "Failed to clear memory:") {
		t.Fatalf("stderr=%q", stderr.String())
	}
}

func TestOpenMemoryStoreRunsConfiguredGC(t *testing.T) {
	home, workspace := t.TempDir(), t.TempDir()
	t.Setenv("GROK_HOME", home)
	orphan := filepath.Join(home, "memory", "old-orphan")
	if err := os.MkdirAll(orphan, 0o700); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * 24 * time.Hour)
	if err := os.Chtimes(orphan, old, old); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Memory: memory.DefaultConfig()}
	cfg.Memory.Enabled = true
	cfg.Memory.GC.MaxAgeDays = 1
	store, err := openMemoryStore(cfg, workspace, "gc-open")
	if err != nil || store == nil {
		t.Fatalf("store=%#v err=%v", store, err)
	}
	if !store.IsEphemeral() {
		t.Fatal("runtime temp workspace was not marked ephemeral")
	}
	deadline := time.Now().Add(time.Second)
	for {
		_, err := os.Stat(orphan)
		if errors.Is(err, os.ErrNotExist) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("orphan remains: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestInteractiveMemoryListDoesNotRunNormalTurn(t *testing.T) {
	store, err := memory.Open(t.TempDir(), t.TempDir(), "interactive-list")
	if err != nil {
		t.Fatal(err)
	}
	path, _, err := store.Write("user_requested", "## Decision\n\nList explicitly.")
	if err != nil {
		t.Fatal(err)
	}
	config := memory.DefaultConfig()
	config.Enabled = true
	runner := &agent.Runner{Memory: store, MemoryConfig: config}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	input := newTerminalInput(ctx, bufio.NewReader(strings.NewReader("/memory\n/exit\n")))
	var stderr bytes.Buffer
	if err := interactiveLoop(ctx, runner, newScheduledWakeQueue(), input, io.Discard, &stderr, "", "response-1"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "memory session") || !strings.Contains(stderr.String(), path) {
		t.Fatalf("stderr=%q", stderr.String())
	}
}

func TestInteractiveMemoryToggleIsSessionScoped(t *testing.T) {
	root, cwd := t.TempDir(), t.TempDir()
	store, err := memory.Open(root, cwd, "interactive-toggle")
	if err != nil {
		t.Fatal(err)
	}
	cfg := memory.DefaultConfig()
	cfg.Enabled = true
	runner := &agent.Runner{Memory: store, MemoryConfig: cfg, OpenMemory: func() (*memory.Store, error) { return memory.Open(root, cwd, "interactive-toggle") }}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	input := newTerminalInput(ctx, bufio.NewReader(strings.NewReader("/mem off\n/memory on\n/exit\n")))
	var stderr bytes.Buffer
	if err := interactiveLoop(ctx, runner, newScheduledWakeQueue(), input, io.Discard, &stderr, "", "response-1"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "Memory disabled for this session.") || !strings.Contains(stderr.String(), "Memory enabled for this session.") || runner.Memory == nil || !runner.MemoryConfig.Enabled {
		t.Fatalf("stderr=%q enabled=%v", stderr.String(), runner.MemoryConfig.Enabled)
	}
}

func TestInteractiveRememberReviewsAndSavesEnhancedGlobalNote(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	streamer := &memoryCommandStreamer{}
	runner := &agent.Runner{Client: streamer, MemoryConfig: memory.DefaultConfig()}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	input := newTerminalInput(ctx, bufio.NewReader(strings.NewReader("/remember run release checks\ne\n/exit\n")))
	var stderr bytes.Buffer
	if err := interactiveLoop(ctx, runner, newScheduledWakeQueue(), input, io.Discard, &stderr, "", "response-1"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(home, "memory", "MEMORY.md"))
	if err != nil || string(data) != "## Decision\n\nFlush explicitly." {
		t.Fatalf("memory=%q err=%v", data, err)
	}
	if !strings.Contains(stderr.String(), "Memory note (raw)") || !strings.Contains(stderr.String(), "Memory note (enhanced)") || !strings.Contains(stderr.String(), "Memory saved to") {
		t.Fatalf("stderr=%q", stderr.String())
	}
	if streamer.request.PreviousResponseID != "" || !strings.Contains(streamer.request.Instructions, "memory note formatter") {
		t.Fatalf("request=%#v", streamer.request)
	}
}

func TestInteractiveDreamConsolidatesWithoutNormalTurn(t *testing.T) {
	root, cwd := t.TempDir(), t.TempDir()
	prior, err := memory.Open(root, cwd, "prior")
	if err != nil {
		t.Fatal(err)
	}
	path, _, err := prior.Write("session_end", "## Decision\n\nKeep this knowledge.")
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-10 * time.Minute)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	store, err := memory.Open(root, cwd, "current")
	if err != nil {
		t.Fatal(err)
	}
	cfg := memory.DefaultConfig()
	cfg.Enabled = true
	streamer := &memoryCommandStreamer{}
	runner := &agent.Runner{Client: streamer, Model: "test", Memory: store, MemoryConfig: cfg}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	input := newTerminalInput(ctx, bufio.NewReader(strings.NewReader("/dream\n/exit\n")))
	var stderr bytes.Buffer
	if err := interactiveLoop(ctx, runner, newScheduledWakeQueue(), input, io.Discard, &stderr, "", "response-1"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "memory dream: written") || !strings.Contains(streamer.request.Instructions, "reflective pass") || streamer.request.PreviousResponseID != "" {
		t.Fatalf("stderr=%q request=%#v", stderr.String(), streamer.request)
	}
}

func TestSessionObserversPersistOnlyLifecycleEvents(t *testing.T) {
	logger, err := session.NewLoggerWithID(t.TempDir(), "parent-session")
	if err != nil {
		t.Fatal(err)
	}
	observer := &sessionSubagentObserver{sessionID: logger.ID(), logger: logger}
	observer.SubagentStarted(context.Background(), subagent.Started{
		ID: "child-1", Type: "explore", Description: "find code", Model: "test", CapabilityMode: "read-only",
	})
	observer.SubagentProgress(context.Background(), tools.SubagentResult{ID: "child-1", Status: "running", Turns: 1})
	observer.SubagentEnded(context.Background(), tools.SubagentResult{ID: "child-1", Type: "explore", Status: "completed", Output: "done"})
	scheduled := newScheduledWakeQueue()
	processObserver := &sessionProcessObserver{sessionID: logger.ID(), logger: logger, scheduler: scheduled}
	processObserver.TaskBackgrounded(tools.ProcessBackgrounded{TaskID: "task-1", Command: "build", CWD: "/work"})
	processObserver.MonitorEvent(tools.MonitorEvent{TaskID: "task-1", Description: "watch build", EventText: "tick"})
	processObserver.TaskCompleted(tools.ProcessSnapshot{TaskID: "task-1", Command: "build", Completed: true})
	processObserver.ScheduledTaskCreated(tools.ScheduledTaskCreated{TaskID: "loop-1", Prompt: "check", HumanSchedule: "every 1 minute"})
	processObserver.ScheduledTaskFired(tools.ScheduledTaskFired{TaskID: "loop-1", Prompt: "check", HumanSchedule: "every 1 minute"})
	processObserver.ScheduledTaskRemoved("loop-1")
	(&sessionGoalObserver{logger: logger}).GoalEvent(tools.GoalEvent{Kind: "goal_planner_fired", Data: map[string]any{"attempt": 1}})
	if event, ok := scheduled.Take(); !ok || event.TaskID != "loop-1" {
		t.Fatalf("scheduled observer event=%#v ok=%v", event, ok)
	} else {
		scheduled.Done(event.TaskID)
	}
	path := logger.Path()
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	log := string(data)
	if strings.Count(log, `"kind":"subagent_spawned"`) != 1 || strings.Count(log, `"kind":"subagent_finished"`) != 1 || strings.Count(log, `"kind":"task_backgrounded"`) != 1 || strings.Count(log, `"kind":"task_completed"`) != 1 || strings.Count(log, `"kind":"scheduled_task_created"`) != 1 || strings.Count(log, `"kind":"scheduled_task_deleted"`) != 1 || strings.Count(log, `"kind":"goal_planner_fired"`) != 1 || strings.Contains(log, "subagent_progress") || strings.Contains(log, "watch build") || strings.Contains(log, "scheduled_task_fired") {
		t.Fatalf("log=%s", log)
	}
}

func TestLocalObserversTrackQueueAndCancelAutoWakes(t *testing.T) {
	queue := newScheduledWakeQueue()
	processes := &sessionProcessObserver{autoWake: true, wake: queue}
	processes.TaskBackgrounded(tools.ProcessBackgrounded{TaskID: "task-1", Command: "build"})
	if !queue.ShouldWait() {
		t.Fatal("background process was not tracked")
	}
	exitCode := 0
	processes.TaskCompleted(tools.ProcessSnapshot{TaskID: "task-1", Command: "build", ExitCode: &exitCode, Completed: true})
	event, ok := queue.Take()
	if !ok || !strings.Contains(event.Prompt, "completed successfully") || !strings.Contains(event.Prompt, "get_task_output") {
		t.Fatalf("process wake=%#v ok=%v", event, ok)
	}
	queue.Done(event.TaskID)

	processes.TaskBackgrounded(tools.ProcessBackgrounded{TaskID: "task-2", Command: "wait"})
	processes.TaskCompleted(tools.ProcessSnapshot{TaskID: "task-2", Command: "wait", ExitCode: &exitCode, BlockWaited: true, Completed: true})
	if _, ok := queue.Take(); ok || queue.ShouldWait() {
		t.Fatal("block-waited process queued an automatic wake")
	}

	subagents := &sessionSubagentObserver{autoWake: true, wake: queue}
	subagents.SubagentStarted(context.Background(), subagent.Started{ID: "child-1", Type: "explore", Description: "inspect", Background: true})
	if !queue.ShouldWait() {
		t.Fatal("background subagent was not tracked")
	}
	result := tools.SubagentResult{ID: "child-1", Type: "explore", Description: "inspect", Status: "completed", WillWake: queue.QueueWake("child-1", formatLocalSubagentWake(tools.SubagentResult{ID: "child-1", Type: "explore", Description: "inspect", Status: "completed"}))}
	subagents.SubagentEnded(context.Background(), result)
	event, ok = queue.Take()
	if !ok || !strings.Contains(event.Prompt, "Background subagent") {
		t.Fatalf("subagent wake=%#v ok=%v", event, ok)
	}
	queue.Done(event.TaskID)
	subagents.SubagentStarted(context.Background(), subagent.Started{ID: "child-2", Background: true})
	subagents.SubagentEnded(context.Background(), tools.SubagentResult{ID: "child-2", Status: "cancelled"})
	if queue.ShouldWait() {
		t.Fatal("cancelled subagent remained tracked")
	}
}

func TestLocalObserversIgnoreBackgroundWorkWhenAutoWakeDisabled(t *testing.T) {
	queue := newScheduledWakeQueue()
	processes := &sessionProcessObserver{wake: queue}
	processes.TaskBackgrounded(tools.ProcessBackgrounded{TaskID: "task-1"})
	exitCode := 0
	processes.TaskCompleted(tools.ProcessSnapshot{TaskID: "task-1", ExitCode: &exitCode, Completed: true})
	subagents := &sessionSubagentObserver{wake: queue}
	subagents.SubagentStarted(context.Background(), subagent.Started{ID: "child-1", Background: true})
	subagents.SubagentEnded(context.Background(), tools.SubagentResult{ID: "child-1", Status: "completed"})
	if _, ok := queue.Take(); ok || queue.ShouldWait() {
		t.Fatal("disabled auto-wake retained or queued background work")
	}
}

func TestParseGoalBudget(t *testing.T) {
	valid := map[string]struct {
		objective string
		budget    int64
	}{
		"do x --budget 1":      {"do x", 1},
		"do x --budget   77":   {"do x", 77},
		"do x \t --budget 500": {"do x", 500},
	}
	for input, want := range valid {
		objective, budget := parseGoalBudget(input)
		if objective != want.objective || budget != want.budget {
			t.Errorf("parseGoalBudget(%q)=(%q,%d), want (%q,%d)", input, objective, budget, want.objective, want.budget)
		}
	}
	invalid := []string{
		"implement X --budget abc", "implement X --budget", "implement X --budget 0",
		"implement X --budget -5", "implement X --budget +5", "implement X --budget 99999999999999999999",
		"implement X --budget5", "tune my-fund--budget 100", "fix the --budget flag parsing bug", "--budget 500000",
	}
	for _, input := range invalid {
		if objective, budget := parseGoalBudget(input); objective != input || budget != 0 {
			t.Errorf("parseGoalBudget(%q)=(%q,%d), want verbatim objective", input, objective, budget)
		}
	}
}

func TestGoalLoopPausesAfterInfrastructureFailure(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry(ws, tools.PromptApprover{Mode: tools.PermissionAuto})
	defer registry.Close()
	if err := registry.BeginGoal("keep state after failure"); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("upstream unavailable")
	runner := &agent.Runner{Client: failingGoalStreamer{err: failure}, Tools: registry, Model: "test"}
	err = goalLoop(context.Background(), runner, registry, io.Discard, io.Discard, "work", "", 1, 3, 10, 8)
	if !errors.Is(err, failure) {
		t.Fatalf("goal loop err=%v", err)
	}
	if snapshot := registry.GoalSnapshot(); snapshot.Status != "infra_paused" || snapshot.Message != "Turn failed: upstream unavailable" {
		t.Fatalf("snapshot=%#v", snapshot)
	}
}

func TestGoalLoopUserCancellationPausesGoal(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry(ws, tools.PromptApprover{Mode: tools.PermissionAuto})
	defer registry.Close()
	if err := registry.BeginGoal("pause safely on cancel"); err != nil {
		t.Fatal(err)
	}
	runner := &agent.Runner{Client: failingGoalStreamer{err: context.Canceled}, Tools: registry, Model: "test"}
	err = goalLoop(context.Background(), runner, registry, io.Discard, io.Discard, "work", "", 1, 3, 10, 8)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("goal loop err=%v", err)
	}
	if snapshot := registry.GoalSnapshot(); snapshot.Status != "user_paused" || snapshot.Message != "" {
		t.Fatalf("snapshot=%#v", snapshot)
	}
}

func TestAppendGoalNextStep(t *testing.T) {
	if got := appendGoalNextStep("continue", "run the integration test"); !strings.Contains(got, "Next step:\nrun the integration test") {
		t.Fatalf("concrete continuation=%q", got)
	}
	if got := appendGoalNextStep("continue", ""); !strings.Contains(got, "Check your `todo_write` list for next steps.") {
		t.Fatalf("fallback continuation=%q", got)
	}
}

func TestAppendGoalVerificationGaps(t *testing.T) {
	if got := appendGoalVerificationGaps("continue", ""); got != "continue" {
		t.Fatalf("empty gaps changed prompt: %q", got)
	}
	got := appendGoalVerificationGaps("continue", "- test still fails")
	if !strings.Contains(got, "Verification REJECTED") || !strings.Contains(got, "- test still fails") {
		t.Fatalf("gaps prompt=%q", got)
	}
}

func TestAppendGoalScratchReminder(t *testing.T) {
	if got := appendGoalScratchReminder("continue", "", false); got != "continue" {
		t.Fatalf("empty scratch changed prompt: %q", got)
	}
	got := appendGoalScratchReminder("continue", "/private/session/implementer", true)
	if !strings.Contains(got, "/private/session/implementer") || !strings.Contains(got, "has been created for you") || !strings.Contains(got, "never shared /tmp") || !strings.Contains(got, "`{SCRATCH}` placeholder") {
		t.Fatalf("scratch prompt=%q", got)
	}
}

func TestAppendGoalReverifyReminder(t *testing.T) {
	if got := appendGoalReverifyReminder("continue", ""); got != "continue" {
		t.Fatalf("empty reminder changed prompt: %q", got)
	}
	if got := appendGoalReverifyReminder("continue", "re-verify now"); got != "continue\n\nre-verify now" {
		t.Fatalf("reminder prompt=%q", got)
	}
}

func TestSessionMCPRuntimeMergesAndRestoresConfiguration(t *testing.T) {
	disabled := false
	runtime := &sessionMCPRuntime{base: config.Config{MCPServers: map[string]config.MCPServerConfig{
		"base":     {Command: "base-server"},
		"disabled": {Command: "disabled-server", Enabled: &disabled},
	}, DisabledMCPServers: []string{"client-disabled"}, DisabledMCPTools: map[string][]string{"base": {"hidden"}}}}
	_, effective, catalog := runtime.mergedConfig([]mcp.ServerConfig{
		{Name: "base", Command: "client-override"},
		{Name: "client-disabled", Command: "client-server"},
		{Name: "extra", Command: "extra-server"},
	})
	if len(effective) != 2 || effective[0].Name != "base" || effective[0].Command != "client-override" || effective[1].Name != "extra" {
		t.Fatalf("unexpected effective MCP configuration: %#v", effective)
	}
	if len(catalog) != 4 || catalog[0].Name != "base" || !slices.Equal(catalog[0].DisabledTools, []string{"hidden"}) || catalog[1].Name != "client-disabled" || !catalog[1].Disabled || catalog[2].Name != "disabled" || !catalog[2].Disabled {
		t.Fatalf("unexpected MCP catalog: %#v", catalog)
	}

	root := t.TempDir()
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry(ws, nil)
	live := newSessionMCPRuntime(context.Background(), config.Config{}, root, registry, nil, nil, io.Discard)
	var changes [][2][]mcp.ServerConfig
	live.SetNotify(func(before, after []mcp.ServerConfig) {
		changes = append(changes, [2][]mcp.ServerConfig{before, after})
	})
	defer func() {
		live.Close()
		_ = registry.Close()
	}()
	if err := live.Update(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	var progress [][2]int
	live.SetProgress(func(total, connected int) {
		progress = append(progress, [2]int{total, connected})
	})
	err = live.Update(context.Background(), []mcp.ServerConfig{{Name: "broken", Command: filepath.Join(root, "missing-server")}})
	if err == nil {
		t.Fatal("invalid MCP update unexpectedly succeeded")
	}
	if !reflect.DeepEqual(progress, [][2]int{{1, 0}, {1, 1}}) {
		t.Fatalf("failed MCP initialization did not complete progress: %#v", progress)
	}
	if configs := live.Configs(); len(configs) != 0 {
		t.Fatalf("failed update replaced previous MCP configuration: %#v", configs)
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodDelete {
			writer.WriteHeader(http.StatusNoContent)
			return
		}
		var rpc struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.NewDecoder(request.Body).Decode(&rpc); err != nil {
			t.Error(err)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		switch rpc.Method {
		case "initialize":
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"jsonrpc": "2.0", "id": rpc.ID, "result": map[string]any{
					"protocolVersion": "2025-11-25", "capabilities": map[string]any{"tools": map[string]any{}},
					"serverInfo": map[string]any{"name": "hot-base", "version": "1"},
				},
			})
		case "notifications/initialized":
			writer.WriteHeader(http.StatusAccepted)
		case "tools/list":
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"jsonrpc": "2.0", "id": rpc.ID, "result": map[string]any{"tools": []any{
					map[string]any{"name": "visible", "inputSchema": map[string]any{"type": "object"}},
					map[string]any{"name": "hidden", "inputSchema": map[string]any{"type": "object"}},
				}},
			})
		default:
			t.Errorf("unexpected MCP method %q", rpc.Method)
			writer.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer server.Close()
	progress = nil
	live.SetProgress(func(total, connected int) {
		progress = append(progress, [2]int{total, connected})
	})
	if err := live.Update(context.Background(), []mcp.ServerConfig{{Name: "client-only", Type: "http", URL: server.URL}}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(progress, [][2]int{{1, 0}, {1, 1}}) {
		t.Fatalf("unexpected MCP initialization progress: %#v", progress)
	}
	if len(changes) != 1 || len(changes[0][0]) != 0 || len(changes[0][1]) != 1 || changes[0][1][0].Name != "client-only" {
		t.Fatalf("unexpected MCP runtime change callback: %#v", changes)
	}
	if err := live.UpdateBase(context.Background(), config.Config{MCPServers: map[string]config.MCPServerConfig{
		"hot-base": {Type: "http", URL: server.URL},
	}, DisabledMCPTools: map[string][]string{"hot-base": {"hidden"}}}); err != nil {
		t.Fatal(err)
	}
	if configs := live.Configs(); len(configs) != 2 || configs[0].Name != "client-only" || configs[1].Name != "hot-base" {
		t.Fatalf("hot base was not applied: %#v", configs)
	}
	var mcpTools []string
	for _, tool := range registry.SnapshotTools() {
		if serverName, ok := tool.(interface{ MCPServerName() string }); ok && serverName.MCPServerName() == "hot-base" {
			mcpTools = append(mcpTools, tool.Definition().Name)
		}
	}
	if len(mcpTools) != 1 || !strings.Contains(mcpTools[0], "visible") {
		t.Fatalf("disabled MCP tool was registered: %#v", mcpTools)
	}
	if err := live.UpdateBase(context.Background(), config.Config{}); err != nil {
		t.Fatal(err)
	}
	if len(changes) != 3 || len(changes[2][0]) != 2 || len(changes[2][1]) != 1 || changes[2][1][0].Name != "client-only" {
		t.Fatalf("MCP base removal callback=%#v", changes)
	}
	if configs := live.Configs(); len(configs) != 1 || configs[0].Name != "client-only" {
		t.Fatalf("hot base removal did not preserve client configuration: %#v", configs)
	}
	err = live.UpdateBase(context.Background(), config.Config{MCPServers: map[string]config.MCPServerConfig{
		"broken-base": {Command: filepath.Join(root, "missing-base-server")},
	}})
	if err == nil {
		t.Fatal("invalid MCP base update unexpectedly succeeded")
	}
	if configs := live.Configs(); len(live.base.MCPServers) != 0 || len(configs) != 1 || configs[0].Name != "client-only" {
		t.Fatalf("failed base update was not rolled back: base=%#v effective=%#v", live.base.MCPServers, live.Configs())
	}
}

func TestSessionMCPRuntimeKeepsSDKServersAcrossUpdates(t *testing.T) {
	root := t.TempDir()
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry(ws, nil)
	defer registry.Close()
	initializeCalls := 0
	failNextList := false
	reverse := func(_ context.Context, _ string, payload json.RawMessage) (json.RawMessage, error) {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.Unmarshal(payload, &request); err != nil {
			return nil, err
		}
		var result any
		switch request.Method {
		case "initialize":
			initializeCalls++
			result = map[string]any{
				"protocolVersion": "2025-11-25",
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "sdk-tools", "version": "1"},
			}
		case "tools/list":
			if failNextList {
				failNextList = false
				return nil, errors.New("SDK tools unavailable")
			}
			result = map[string]any{"tools": []any{map[string]any{
				"name": "echo", "inputSchema": map[string]any{"type": "object"},
			}}}
		case "tools/call":
			result = map[string]any{"content": []any{map[string]any{"type": "text", "text": "sdk echo"}}}
		default:
			return nil, fmt.Errorf("unexpected SDK MCP method %q", request.Method)
		}
		return json.Marshal(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
	}
	runtime := newSessionMCPRuntime(context.Background(), config.Config{}, root, registry, nil, nil, io.Discard)
	runtime.SetSDKServers([]mcp.ServerConfig{{Type: "acp", Name: "sdk-tools", ServerID: "srv-0"}}, reverse)
	var progress [][2]int
	runtime.SetProgress(func(total, connected int) { progress = append(progress, [2]int{total, connected}) })
	defer runtime.Close()
	if err := runtime.Update(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(progress, [][2]int{{1, 0}, {1, 1}}) {
		t.Fatalf("SDK MCP progress=%#v", progress)
	}
	configs, catalog := runtime.Configs(), runtime.Catalog()
	if len(configs) != 1 || configs[0].Name != "sdk-tools" || configs[0].ServerID != "srv-0" || len(catalog) != 1 {
		t.Fatalf("SDK MCP effective=%#v catalog=%#v", configs, catalog)
	}
	var sdkTool tools.Tool
	for _, tool := range registry.SnapshotTools() {
		if marker, ok := tool.(interface{ MCPServerName() string }); ok && marker.MCPServerName() == "sdk-tools" {
			sdkTool = tool
			break
		}
	}
	if sdkTool == nil {
		t.Fatalf("SDK MCP tool was not registered: %#v", registry.SnapshotTools())
	}
	output, err := sdkTool.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil || output != "sdk echo" {
		t.Fatalf("SDK MCP tool output=%q err=%v", output, err)
	}
	progress = nil
	if err := runtime.Update(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if initializeCalls != 2 || !reflect.DeepEqual(progress, [][2]int{{1, 0}, {1, 1}}) {
		t.Fatalf("SDK MCP was not rebuilt: initialize=%d progress=%#v", initializeCalls, progress)
	}
	progress = nil
	failNextList = true
	if err := runtime.Update(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "SDK tools unavailable") {
		t.Fatalf("SDK MCP discovery error=%v", err)
	}
	if !reflect.DeepEqual(progress, [][2]int{{1, 0}, {1, 1}}) || len(runtime.Configs()) != 1 || initializeCalls != 4 {
		t.Fatalf("failed SDK MCP update was not completed and restored: progress=%#v configs=%#v initialize=%d", progress, runtime.Configs(), initializeCalls)
	}
	if err := runtime.UpdateBase(context.Background(), config.Config{DisabledMCPTools: map[string][]string{"sdk-tools": {"echo"}}}); err != nil {
		t.Fatal(err)
	}
	for _, tool := range registry.SnapshotTools() {
		if marker, ok := tool.(interface{ MCPServerName() string }); ok && marker.MCPServerName() == "sdk-tools" {
			t.Fatalf("disabled SDK MCP tool remains registered: %s", tool.Definition().Name)
		}
	}
	if err := runtime.UpdateBase(context.Background(), config.Config{DisabledMCPServers: []string{"sdk-tools"}}); err != nil {
		t.Fatal(err)
	}
	if len(runtime.Configs()) != 0 || len(runtime.Catalog()) != 1 || !runtime.Catalog()[0].Disabled {
		t.Fatalf("disabled SDK MCP state effective=%#v catalog=%#v", runtime.Configs(), runtime.Catalog())
	}
}

func TestModelCacheIdentityFollowsAuthenticationRoute(t *testing.T) {
	cfg := config.Config{BaseURL: "https://api.x.ai/v1/", ProxyBaseURL: "https://proxy.example/v1/"}
	if auth, origin := modelCacheIdentity(cfg, nil); auth != "api_key" || origin != "https://api.x.ai/v1/models" {
		t.Fatalf("api identity=%q %q", auth, origin)
	}
	cfg.DeploymentKey = "deployment"
	if auth, origin := modelCacheIdentity(cfg, nil); auth != "deployment" || origin != "https://proxy.example/v1/models" {
		t.Fatalf("deployment identity=%q %q", auth, origin)
	}
	provider := func(context.Context, string) (string, error) { return "token", nil }
	if auth, origin := modelCacheIdentity(cfg, provider); auth != "session" || origin != "https://proxy.example/v1/models" {
		t.Fatalf("session identity=%q %q", auth, origin)
	}
}

func TestFetchACPModelCacheUsesSelectedCredentials(t *testing.T) {
	tests := []struct {
		name       string
		authMethod string
		apiKey     string
		deployment string
		wantToken  string
		wantHeader string
		wantUser   string
	}{
		{name: "deployment", authMethod: "deployment", apiKey: "api-key", deployment: "deployment-key", wantToken: "deployment-key"},
		{name: "session", authMethod: "session", apiKey: "session-token", wantToken: "session-token", wantHeader: auth.DefaultTokenHeader, wantUser: "user-1"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("GROK_HOME", t.TempDir())
			authPath := filepath.Join(t.TempDir(), "auth.json")
			scope := "issuer::client"
			if test.authMethod == "session" {
				if err := auth.Save(authPath, scope, auth.Credential{Key: "stored-token", UserID: "user-1", Email: "user@example.com"}); err != nil {
					t.Fatal(err)
				}
			}
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if got := request.Header.Get("Authorization"); got != "Bearer "+test.wantToken {
					t.Errorf("authorization=%q", got)
				}
				if got := request.Header.Get("X-XAI-Token-Auth"); got != test.wantHeader {
					t.Errorf("token auth=%q", got)
				}
				if got := request.Header.Get("x-userid"); got != test.wantUser {
					t.Errorf("user=%q", got)
				}
				fmt.Fprint(writer, `{"data":[{"id":"grok","model":"grok"}]}`)
			}))
			defer server.Close()
			cfg := config.Config{APIKey: test.apiKey, DeploymentKey: test.deployment, BaseURL: server.URL + "/v1", HTTPTimeout: time.Second}
			if _, err := fetchACPModelCache(context.Background(), cfg, test.authMethod, server.URL+"/models", authPath, scope); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestShouldClearACPModelCacheOnlyForCurrentSessionLogout(t *testing.T) {
	if !shouldClearACPModelCache("session", auth.LogoutResult{ClearedCurrent: true}) {
		t.Fatal("current session logout did not clear cache")
	}
	for _, test := range []struct {
		authMethod string
		result     auth.LogoutResult
	}{
		{authMethod: "session", result: auth.LogoutResult{}},
		{authMethod: "api_key", result: auth.LogoutResult{ClearedCurrent: true}},
		{authMethod: "deployment", result: auth.LogoutResult{ClearedCurrent: true}},
	} {
		if shouldClearACPModelCache(test.authMethod, test.result) {
			t.Fatalf("cache cleared for auth=%q result=%#v", test.authMethod, test.result)
		}
	}
}

func TestClearACPLogoutPolicy(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	managed := filepath.Join(home, "managed_config.toml")
	writeManaged := func() {
		if err := os.WriteFile(managed, []byte("[ui]\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeManaged()
	team := auth.LogoutResult{WasLoggedIn: true, ClearedCurrent: true, Credential: auth.Credential{TeamID: "team-1"}}
	if err := clearACPLogoutPolicy(context.Background(), config.Config{}, team); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(managed); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("team policy still exists: %v", err)
	}

	for _, test := range []struct {
		name   string
		cfg    config.Config
		result auth.LogoutResult
	}{
		{name: "sibling scope", result: auth.LogoutResult{WasLoggedIn: true, Credential: auth.Credential{TeamID: "team-1"}}},
		{name: "personal account", result: auth.LogoutResult{WasLoggedIn: true, ClearedCurrent: true}},
		{name: "deployment policy", cfg: config.Config{DeploymentKey: "deployment"}, result: team},
	} {
		t.Run(test.name, func(t *testing.T) {
			writeManaged()
			if err := clearACPLogoutPolicy(context.Background(), test.cfg, test.result); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(managed); err != nil {
				t.Fatalf("policy was cleared: %v", err)
			}
		})
	}

	writeManaged()
	if err := clearACPLogoutPolicy(context.Background(), config.Config{}, auth.LogoutResult{ClearedCurrent: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(managed); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("externally cleared auth left policy: %v", err)
	}
}

func TestDiscoverSkillsLoadsConfiguredPlugin(t *testing.T) {
	root := t.TempDir()
	pluginRoot := filepath.Join(root, "plugin")
	skillDir := filepath.Join(pluginRoot, "skills", "deploy")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginRoot, "plugin.json"), []byte(`{"name":"team-tools","mcpServers":{"plugin-mcp":{"command":"${GROK_PLUGIN_ROOT}/server"}},"lspServers":{"plugin-lsp":{"command":"gopls","extensions":{".go":"go"}}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: deploy\ndescription: Deploy\n---\nDeploy"), 0o600); err != nil {
		t.Fatal(err)
	}
	pluginRoot, _ = filepath.EvalSymlinks(pluginRoot)
	workspaceCfg, catalog, _, err := discoverWorkspace(root, config.Config{Compat: compat.Default(), Plugins: config.PluginsConfig{Paths: []string{pluginRoot}}}, true)
	if err != nil {
		t.Fatal(err)
	}
	if names := strings.Join(catalog.Names(), "|"); names != "team-tools:deploy" {
		t.Fatalf("plugin skill names = %q", names)
	}
	if workspaceCfg.MCPServers["plugin-mcp"].Command != filepath.Join(pluginRoot, "server") {
		t.Fatalf("plugin MCP config = %#v", workspaceCfg.MCPServers)
	}
	if workspaceCfg.LSPServers["plugin-lsp"].Command != "gopls" || strings.Join(workspaceCfg.LSPServers["plugin-lsp"].Extensions, "|") != ".go" {
		t.Fatalf("plugin LSP config = %#v", workspaceCfg.LSPServers)
	}
}

func TestStartLSPServersRegistersDynamicToolWithoutInitialServers(t *testing.T) {
	root := t.TempDir()
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry(ws, nil)
	defer registry.Close()
	manager, err := startLSPServers(context.Background(), config.Config{}, ws, registry, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	if len(manager.Names()) != 0 {
		t.Fatalf("unexpected initial LSP servers: %#v", manager.Names())
	}
	found := false
	for _, tool := range registry.SnapshotTools() {
		if tool.Definition().Name == "lsp" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("dynamic LSP tool was not registered")
	}
}

func TestWatchMCPConfigReloadsChangedFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".mcp.json")
	if err := os.WriteFile(path, []byte(`{"mcpServers":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reloaded := make(chan struct{}, 1)
	watchMCPConfig(ctx, 5*time.Millisecond, func() ([]string, error) {
		return []string{path}, nil
	}, func() error {
		reloaded <- struct{}{}
		return nil
	}, io.Discard)
	if err := os.WriteFile(path, []byte(`{"mcpServers":{"added":{"command":"server"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-reloaded:
	case <-time.After(time.Second):
		t.Fatal("MCP config change was not reloaded")
	}
}

func TestWatchModelConfigRetriesFailedReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("model = \"old\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	attempts := make(chan int, 2)
	count := 0
	watchModelConfig(ctx, 5*time.Millisecond, []string{path}, func() error {
		count++
		attempts <- count
		if count == 1 {
			return errors.New("temporary failure")
		}
		return nil
	}, io.Discard)
	if err := os.WriteFile(path, []byte("model = \"new\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for want := 1; want <= 2; want++ {
		select {
		case got := <-attempts:
			if got != want {
				t.Fatalf("attempt=%d want=%d", got, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("model reload attempt %d did not run", want)
		}
	}
}

func TestRunPluginLifecycle(t *testing.T) {
	grokHome := filepath.Join(t.TempDir(), ".grok")
	t.Setenv("GROK_HOME", grokHome)
	source := filepath.Join(t.TempDir(), "source")
	if err := os.MkdirAll(filepath.Join(source, "skills", "cli"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "plugin.json"), []byte(`{"name":"cli-plugin","version":"1.0.0"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "skills", "cli", "SKILL.md"), []byte("---\nname: cli\ndescription: CLI\n---\nCLI"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if err := runPlugin([]string{"install", source}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "cli-plugin") {
		t.Fatalf("install output = %q", stdout.String())
	}
	cfg, err := config.Load("")
	if err != nil || strings.Join(cfg.Plugins.Enabled, "|") != "cli-plugin" {
		t.Fatalf("installed config=%#v err=%v", cfg.Plugins, err)
	}
	stdout.Reset()
	if err := runPlugin([]string{"list"}, &stdout, &stderr); err != nil || !strings.Contains(stdout.String(), "cli-plugin") {
		t.Fatalf("list output=%q err=%v", stdout.String(), err)
	}
	if err := os.WriteFile(filepath.Join(source, "plugin.json"), []byte(`{"name":"cli-plugin","version":"2.0.0"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	if err := runPlugin([]string{"update", "cli-plugin"}, &stdout, &stderr); err != nil || !strings.Contains(stdout.String(), "updated") {
		t.Fatalf("update output=%q err=%v", stdout.String(), err)
	}
	stdout.Reset()
	if err := runPlugin([]string{"uninstall", "cli-plugin"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	cfg, err = config.Load("")
	if err != nil || len(cfg.Plugins.Enabled) != 0 || !strings.Contains(stdout.String(), "Uninstalled") {
		t.Fatalf("uninstall output=%q config=%#v err=%v", stdout.String(), cfg.Plugins, err)
	}
}

func TestApplyMarketplacePlugins(t *testing.T) {
	settings := plugin.Settings{Enabled: []string{"old", "keep"}, Disabled: []string{"new"}}
	applyMarketplacePlugins(&settings, "update", marketplace.Outcome{Plugins: []string{"new"}, RemovedPlugins: []string{"old"}})
	if strings.Join(settings.Enabled, "|") != "keep|new" || len(settings.Disabled) != 0 {
		t.Fatalf("updated marketplace settings=%#v", settings)
	}
	applyMarketplacePlugins(&settings, "uninstall", marketplace.Outcome{Plugins: []string{"new"}})
	if strings.Join(settings.Enabled, "|") != "keep" {
		t.Fatalf("uninstalled marketplace settings=%#v", settings)
	}
	settings.Enabled = append(settings.Enabled, "source-plugin")
	settings.Disabled = append(settings.Disabled, "source-disabled")
	applyMarketplacePlugins(&settings, "remove_source", marketplace.Outcome{Plugins: []string{"source-plugin", "source-disabled"}})
	if strings.Join(settings.Enabled, "|") != "keep" || len(settings.Disabled) != 0 {
		t.Fatalf("removed source settings=%#v", settings)
	}
}

func TestMCPHTTPHeadersUseBearerTokenEnvironment(t *testing.T) {
	t.Setenv("MCP_ACCESS_TOKEN", "secret")
	headers := mcpHTTPHeaders(config.MCPServerConfig{
		Headers:           map[string]string{"authorization": "Bearer old", "X-Test": "kept"},
		BearerTokenEnvVar: "MCP_ACCESS_TOKEN",
	})
	if headers["Authorization"] != "Bearer secret" || headers["X-Test"] != "kept" || len(headers) != 2 {
		t.Fatalf("headers = %#v", headers)
	}
}

func TestResolveProjectTrustPromptsAndPersists(t *testing.T) {
	previousVersion := version.Current
	version.Current = "1.0.0"
	t.Cleanup(func() { version.Current = previousVersion })
	t.Setenv("GROK_HOME", t.TempDir())
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".mcp.json"), []byte(`{"mcpServers":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{FolderTrustEnabled: true}
	var output bytes.Buffer
	trusted, err := resolveProjectTrust(context.Background(), root, cfg, false, bufio.NewReader(strings.NewReader("")), &output, false)
	if err != nil || trusted || !strings.Contains(output.String(), "--trust") {
		t.Fatalf("headless trust=%v output=%q err=%v", trusted, output.String(), err)
	}
	output.Reset()
	trusted, err = resolveProjectTrust(context.Background(), root, cfg, false, bufio.NewReader(strings.NewReader("yes\n")), &output, true)
	if err != nil || !trusted || !strings.Contains(output.String(), "Trust executable") {
		t.Fatalf("interactive trust=%v output=%q err=%v", trusted, output.String(), err)
	}
	trusted, err = resolveProjectTrust(context.Background(), root, cfg, false, bufio.NewReader(strings.NewReader("")), &output, false)
	if err != nil || !trusted {
		t.Fatalf("persisted trust=%v err=%v", trusted, err)
	}
}

func (s *samplingStreamer) StreamResponse(_ context.Context, request api.ResponseRequest, _ func(string)) (api.StreamResult, error) {
	s.request = request
	return api.StreamResult{Text: "sampled response"}, nil
}

func TestRunMCPSamplingMapsConversation(t *testing.T) {
	streamer := &samplingStreamer{}
	result, err := runMCPSampling(context.Background(), streamer, "sample-model", mcp.SamplingRequest{
		SystemPrompt: "Be concise", MaxTokens: 128,
		Messages: []mcp.SamplingMessage{
			{Role: "user", Content: mcp.SamplingContent{Type: "text", Text: "question"}},
			{Role: "assistant", Content: mcp.SamplingContent{Type: "text", Text: "prior answer"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Role != "assistant" || result.Content.Text != "sampled response" || result.Model != "sample-model" || result.StopReason != "endTurn" {
		t.Fatalf("unexpected sampling result: %#v", result)
	}
	request := streamer.request
	if request.Model != "sample-model" || request.Instructions != "Be concise" || request.MaxOutputTokens != 128 || len(request.Input) != 2 {
		t.Fatalf("unexpected model request: %#v", request)
	}
	if request.Input[0].Role != "user" || request.Input[0].Content != "question" || request.Input[1].Role != "assistant" {
		t.Fatalf("sampling messages were not preserved: %#v", request.Input)
	}
}

func TestRunMCPSamplingRejectsUnsupportedContent(t *testing.T) {
	_, err := runMCPSampling(context.Background(), &samplingStreamer{}, "model", mcp.SamplingRequest{
		Messages: []mcp.SamplingMessage{{Role: "user", Content: mcp.SamplingContent{Type: "audio"}}},
	})
	if err == nil {
		t.Fatal("unsupported sampling content was accepted")
	}
}

func TestMCPSamplingRequiresApproval(t *testing.T) {
	handler := newMCPSamplingHandler(config.Config{}, tools.PromptApprover{Mode: tools.PermissionDeny}, nil, "fixture")
	_, err := handler(context.Background(), mcp.SamplingRequest{
		MaxTokens: 1, Messages: []mcp.SamplingMessage{{Role: "user", Content: mcp.SamplingContent{Type: "text", Text: "private context"}}},
	})
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("unexpected approval error: %v", err)
	}
}

func TestLoginRejectsConflictingTransportsWithoutNetwork(t *testing.T) {
	err := run([]string{"login", "--oauth", "--device-auth"}, strings.NewReader(""), io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("unexpected login error: %v", err)
	}
}

func TestXAIBaseURLDetection(t *testing.T) {
	if !isXAIBaseURL("https://api.x.ai/v1") || isXAIBaseURL("https://api.x.ai.example/v1") || isXAIBaseURL("https://provider.example/v1") {
		t.Fatal("xAI base URL detection is incorrect")
	}
}

func TestBrowserCommandUsesPlatformLaunchersWithoutShell(t *testing.T) {
	rawURL := "https://accounts.x.ai/device?code=A&B"
	for _, test := range []struct {
		goos    string
		command string
		args    []string
	}{
		{"darwin", "open", []string{rawURL}},
		{"linux", "xdg-open", []string{rawURL}},
		{"windows", "rundll32", []string{"url.dll,FileProtocolHandler", rawURL}},
	} {
		command, args := browserCommand(test.goos, rawURL)
		if command != test.command || strings.Join(args, "\x00") != strings.Join(test.args, "\x00") {
			t.Fatalf("browser command for %s: %q %#v", test.goos, command, args)
		}
	}
	if command, args := browserCommand("linux", ""); command != "" || args != nil {
		t.Fatalf("empty URL should not produce a browser command: %q %#v", command, args)
	}
}

func TestRunLoginDeviceFlow(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/oauth2/device/code":
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"device_code": "device-1", "user_code": "ABCD-1234",
				"verification_uri": "http://127.0.0.1/verify", "expires_in": 600, "interval": 1,
			})
		case "/oauth2/token":
			_ = json.NewEncoder(writer).Encode(map[string]any{"access_token": "access-1", "refresh_token": "refresh-1", "expires_in": 3600})
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	authFile := filepath.Join(t.TempDir(), "auth.json")
	configPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(configPath, []byte("base_url = \""+server.URL+"\"\n[endpoints]\ncli_chat_proxy_base_url = \""+server.URL+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	err := run([]string{
		"login", "--device-auth", "--issuer", server.URL, "--client-id", "client-1", "--scopes", "openid", "--auth-file", authFile, "--config", configPath, "--no-browser",
	}, strings.NewReader(""), &stdout, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "Signed in") || !strings.Contains(stderr.String(), "ABCD-1234") {
		t.Fatalf("unexpected login output: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	credential, err := auth.Load(authFile, (auth.Config{Issuer: server.URL, ClientID: "client-1"}).Scope())
	if err != nil || credential.Key != "access-1" || credential.RefreshToken != "refresh-1" {
		t.Fatalf("stored credential=%#v err=%v", credential, err)
	}
}

func TestRunSetupInstallsManagedConfiguration(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	t.Setenv("GROK_DEPLOYMENT_KEY", "deployment-secret")
	managed := "[models]\ndefault = \"managed\"\n"
	requirements := "[auth]\npreferred_method = \"oidc\"\n"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer deployment-secret" {
			t.Fatalf("setup authorization=%q", request.Header.Get("Authorization"))
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"deployment_id": "deployment-1", "managed_config": managed, "requirements": requirements,
		})
	}))
	defer server.Close()
	path := filepath.Join(home, "config.toml")
	if err := os.WriteFile(path, []byte("[endpoints]\nmanaged_config_url = \""+server.URL+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	if err := run([]string{"setup", "--config", path}, strings.NewReader(""), &stdout, io.Discard); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "Managed configuration updated\n" {
		t.Fatalf("setup output=%q", stdout.String())
	}
	if data, err := os.ReadFile(filepath.Join(home, "managed_config.toml")); err != nil || string(data) != managed {
		t.Fatalf("managed config=%q err=%v", data, err)
	}
	if data, err := os.ReadFile(filepath.Join(home, "requirements.toml")); err != nil || string(data) != requirements {
		t.Fatalf("requirements=%q err=%v", data, err)
	}
}

func TestRunSetupRequiresManagedPrincipal(t *testing.T) {
	t.Setenv("GROK_HOME", t.TempDir())
	t.Setenv("GROK_DEPLOYMENT_KEY", "")
	err := run([]string{"setup"}, strings.NewReader(""), io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "GROK_DEPLOYMENT_KEY") {
		t.Fatalf("setup without principal error=%v", err)
	}
}

func TestRunSetupJSONDoesNotWritePolicy(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	t.Setenv("GROK_DEPLOYMENT_KEY", "deployment-secret")
	managed := "[auth]\npreferred_method = \"oidc\"\n"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"deployment_id": "deployment-1", "managed_config": managed,
		})
	}))
	defer server.Close()
	path := filepath.Join(home, "config.toml")
	if err := os.WriteFile(path, []byte("[endpoints]\nmanaged_config_url = \""+server.URL+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	if err := run([]string{"setup", "--json", "--config", path}, strings.NewReader(""), &stdout, io.Discard); err != nil {
		t.Fatal(err)
	}
	var report struct {
		Source       string `json:"source"`
		Configured   bool   `json:"configured"`
		DeploymentID string `json:"deploymentId"`
		Managed      string `json:"managedConfig"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Source != "deploymentKey" || !report.Configured || report.DeploymentID != "deployment-1" || report.Managed != managed {
		t.Fatalf("setup report=%#v", report)
	}
	if _, err := os.Stat(filepath.Join(home, "managed_config.toml")); !os.IsNotExist(err) {
		t.Fatalf("setup --json wrote policy: %v", err)
	}
}

func TestSessionStartRepairsAndReloadsMissingManagedPolicy(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	t.Setenv("GROK_DEPLOYMENT_KEY", "deployment-secret")
	t.Setenv("GORK_API_KEY", "")
	t.Setenv("XAI_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	requests := 0
	requirements := "[auth]\npreferred_method = \"api_key\"\n"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests++
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"deployment_id": "deployment-1", "requirements": requirements,
		})
	}))
	defer server.Close()
	path := filepath.Join(home, "config.toml")
	data := "[models]\ndefault = \"main\"\n[model.main]\nmodel = \"model\"\nbase_url = \"https://api.x.ai/v1\"\nbackend = \"responses\"\n[endpoints]\nmanaged_config_url = \"" + server.URL + "\"\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	err := run([]string{"--config", path, "hello"}, strings.NewReader(""), io.Discard, io.Discard)
	if requests != 1 || err == nil || !strings.Contains(err.Error(), "missing credentials") {
		t.Fatalf("session repair requests=%d err=%v", requests, err)
	}
	if data, err := os.ReadFile(filepath.Join(home, "requirements.toml")); err != nil || string(data) != requirements {
		t.Fatalf("repaired requirements=%q err=%v", data, err)
	}
}

func TestRunLogoutRemovesSelectedScope(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	selected := auth.Config{Issuer: "https://auth.example", ClientID: "client-1"}
	if err := auth.Save(path, selected.Scope(), auth.Credential{Key: "remove"}); err != nil {
		t.Fatal(err)
	}
	if err := auth.Save(path, "sibling", auth.Credential{Key: "keep"}); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	if err := run([]string{"logout", "--issuer", selected.Issuer, "--client-id", selected.ClientID, "--auth-file", path}, strings.NewReader(""), &stdout, io.Discard); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "Signed out\n" {
		t.Fatalf("logout output=%q", stdout.String())
	}
	if _, err := auth.Load(path, selected.Scope()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("selected scope still loads: %v", err)
	}
	if credential, err := auth.Load(path, "sibling"); err != nil || credential.Key != "keep" {
		t.Fatalf("sibling credential=%#v err=%v", credential, err)
	}
}

func TestRunLogoutClearsTeamManagedPolicy(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	t.Setenv("GROK_DEPLOYMENT_KEY", "")
	authConfig := auth.DefaultConfig()
	if err := auth.Save(filepath.Join(home, "auth.json"), authConfig.Scope(), auth.Credential{Key: "team-token", TeamID: "team-1"}); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{
		"managed_config.toml":      "[auth]\npreferred_method = \"oidc\"\n",
		"requirements.toml":        "[auth]\npreferred_method = \"oidc\"\n",
		"managed_config.sync.json": "{}\n",
	} {
		if err := os.WriteFile(filepath.Join(home, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := run([]string{"logout"}, strings.NewReader(""), io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"managed_config.toml", "requirements.toml", "managed_config.sync.json"} {
		if _, err := os.Stat(filepath.Join(home, name)); !os.IsNotExist(err) {
			t.Fatalf("team policy %s was not removed: %v", name, err)
		}
	}
}

func TestTeamPolicyDisablesStaticAPIKey(t *testing.T) {
	t.Setenv("GROK_HOME", t.TempDir())
	t.Setenv("GORK_API_KEY", "must-not-bypass-team-policy")
	t.Setenv("XAI_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	path := filepath.Join(t.TempDir(), "config.toml")
	data := []byte(`
[models]
default = "main"

[model.main]
model = "model"
base_url = "https://api.x.ai/v1"
backend = "responses"

[grok_com_config]
force_login_team_uuid = "team-required"
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	err := run([]string{"--config", path, "hello"}, strings.NewReader(""), io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "missing credentials") {
		t.Fatalf("static API key bypassed team policy: %v", err)
	}
}

func TestPreferredAuthMethodFailsClosed(t *testing.T) {
	for _, method := range []string{"oidc", "api_key"} {
		t.Run(method, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("GROK_HOME", home)
			t.Setenv("GROK_OIDC_ISSUER", "")
			t.Setenv("GROK_OIDC_CLIENT_ID", "")
			t.Setenv("GROK_OAUTH2_ISSUER", "")
			t.Setenv("GROK_OAUTH2_CLIENT_ID", "")
			t.Setenv("XAI_API_KEY", "")
			t.Setenv("OPENAI_API_KEY", "")
			if method == "oidc" {
				t.Setenv("GORK_API_KEY", "static-must-be-ignored")
			} else {
				t.Setenv("GORK_API_KEY", "")
				path, err := auth.DefaultPath()
				if err != nil {
					t.Fatal(err)
				}
				cfg := auth.DefaultConfig()
				if err := auth.Save(path, cfg.Scope(), auth.Credential{Key: "session-must-be-ignored"}); err != nil {
					t.Fatal(err)
				}
			}
			path := filepath.Join(t.TempDir(), "config.toml")
			data := []byte("[models]\ndefault = \"main\"\n[model.main]\nmodel = \"model\"\nbase_url = \"https://api.x.ai/v1\"\nbackend = \"responses\"\n[auth]\npreferred_method = \"" + method + "\"\n")
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			err := run([]string{"--config", path, "hello"}, strings.NewReader(""), io.Discard, io.Discard)
			if err == nil || !strings.Contains(err.Error(), "missing credentials") {
				t.Fatalf("preferred method %s fell back: %v", method, err)
			}
		})
	}
}

func TestRequirementsDenyCannotBeOverriddenByCLIAllow(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	data := []byte("[[permission.rules]]\naction = \"deny\"\ntool = \"bash\"\npattern = \"git push*\"\n")
	if err := os.WriteFile(filepath.Join(home, "requirements.toml"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(filepath.Join(home, "missing.toml"))
	if err != nil {
		t.Fatal(err)
	}
	allow, ask, deny, err := permissionRules(cfg.Permission, []string{"Bash(*)"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	approver, err := tools.NewPolicyApprover(
		tools.PromptApprover{Mode: tools.PermissionAuto}, tools.PromptApprover{Mode: tools.PermissionAuto},
		allow, ask, deny,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := approver.Approve(context.Background(), "shell", "git push origin main"); err == nil || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("CLI allow bypassed requirements deny: %v", err)
	}
}

func TestPermissionPromptNotifierOnlyWrapsAskPaths(t *testing.T) {
	count := 0
	notifier := &permissionPromptApprover{base: tools.PromptApprover{Mode: tools.PermissionAuto}}
	notifier.SetNotify(func() { count++ })
	policy, err := tools.NewPolicyApprover(
		tools.PromptApprover{Mode: tools.PermissionAuto}, notifier,
		[]string{"Bash(git status)"}, []string{"Bash(git push *)"}, []string{"Bash(rm *)"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := policy.Approve(context.Background(), "shell", "git status"); err != nil || count != 0 {
		t.Fatalf("allow err=%v count=%d", err, count)
	}
	if err := policy.Approve(context.Background(), "shell", "rm file"); err == nil || count != 0 {
		t.Fatalf("deny err=%v count=%d", err, count)
	}
	if err := policy.Approve(context.Background(), "shell", "git push origin main"); err != nil || count != 1 {
		t.Fatalf("ask err=%v count=%d", err, count)
	}
	defaultPrompt, err := tools.NewPolicyApprover(notifier, notifier, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := defaultPrompt.Approve(context.Background(), "shell", "go test ./..."); err != nil || count != 2 {
		t.Fatalf("default prompt err=%v count=%d", err, count)
	}
}

func TestPluginMarketplaceCLI(t *testing.T) {
	t.Setenv("GROK_OFFICIAL_MARKETPLACE_AUTO_REGISTER", "false")
	t.Setenv("GROK_HOME", filepath.Join(t.TempDir(), ".grok"))
	t.Setenv("HOME", t.TempDir())
	source := filepath.Join(t.TempDir(), "team-catalog")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if err := runPlugin([]string{"marketplace", "add", source}, &stdout, &stderr); err != nil || !strings.Contains(stdout.String(), "source added") {
		t.Fatalf("add stdout=%q stderr=%q err=%v", stdout.String(), stderr.String(), err)
	}
	stdout.Reset()
	if err := runPlugin([]string{"marketplace", "list", "--json"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	var listed []map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &listed); err != nil || len(listed) != 1 || listed[0]["name"] != "team-catalog" || listed[0]["kind"] != "local" || listed[0]["source"].(map[string]any)["path"] != source {
		t.Fatalf("listed=%#v output=%q err=%v", listed, stdout.String(), err)
	}
	stdout.Reset()
	if err := runPlugin([]string{"marketplace", "update", "team-catalog"}, &stdout, &stderr); err != nil || !strings.Contains(stdout.String(), "refreshed") {
		t.Fatalf("update stdout=%q err=%v", stdout.String(), err)
	}
	stdout.Reset()
	if err := runPlugin([]string{"marketplace", "remove", source}, &stdout, &stderr); err != nil || !strings.Contains(stdout.String(), "source removed") {
		t.Fatalf("remove stdout=%q err=%v", stdout.String(), err)
	}
	if err := runPlugin([]string{"marketplace", "add", filepath.Join(source, "missing")}, io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("missing local source err=%v", err)
	}
}

func TestPluginMarketplaceCLIAutoRegistersOfficialSource(t *testing.T) {
	t.Setenv("GROK_HOME", filepath.Join(t.TempDir(), ".grok"))
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GROK_OFFICIAL_MARKETPLACE_AUTO_REGISTER", "true")
	var stdout bytes.Buffer
	if err := runPlugin([]string{"marketplace", "list", "--json"}, &stdout, io.Discard); err != nil {
		t.Fatal(err)
	}
	var listed []map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &listed); err != nil || len(listed) != 1 || listed[0]["name"] != marketplace.OfficialSourceName || listed[0]["source"].(map[string]any)["url"] != marketplace.OfficialSourceGit {
		t.Fatalf("listed=%#v output=%q err=%v", listed, stdout.String(), err)
	}
}

func TestGoalResumeDoesNotRequireNewPrompt(t *testing.T) {
	t.Setenv("GORK_API_KEY", "test-key")
	t.Setenv("GORK_MODEL", "test-model")
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	t.Setenv("HOME", home)
	missing := filepath.Join(t.TempDir(), "missing.jsonl")
	err := run([]string{"--goal", "--resume", missing, "--config", filepath.Join(home, "missing.toml"), "--workspace", t.TempDir()}, strings.NewReader(""), io.Discard, io.Discard)
	if err == nil || strings.Contains(err.Error(), "prompt is required") {
		t.Fatalf("resume err=%v", err)
	}
}

type planUIApprover struct {
	entered, exited int
	decision        tools.PlanModeDecision
}

func (*planUIApprover) Approve(context.Context, string, string) error { return nil }
func (p *planUIApprover) PlanModeEntered(tools.PlanModeEvent)         { p.entered++ }
func (p *planUIApprover) PlanModeExited(tools.PlanModeEvent)          { p.exited++ }
func (p *planUIApprover) ApprovePlanModeExit(context.Context, tools.PlanModeEvent) (tools.PlanModeDecision, error) {
	return p.decision, nil
}

func TestSessionProcessObserverDelegatesDedicatedPlanUI(t *testing.T) {
	reviewer := &planUIApprover{decision: tools.PlanModeDecision{Outcome: "cancelled", Feedback: "add rollback"}}
	observer := &sessionProcessObserver{planApprover: reviewer}
	event := tools.PlanModeEvent{PlanContent: "# Plan"}
	observer.PlanModeEntered(event)
	decision, err := observer.ApprovePlanModeExit(context.Background(), event)
	observer.PlanModeExited(event)
	if err != nil || decision != reviewer.decision || reviewer.entered != 1 || reviewer.exited != 1 {
		t.Fatalf("decision=%#v entered=%d exited=%d err=%v", decision, reviewer.entered, reviewer.exited, err)
	}
}

func TestResolveACPSessionPermissionMode(t *testing.T) {
	tests := []struct {
		name                    string
		defaultMode             tools.PermissionMode
		yoloMode, autoMode      *bool
		disableBypass, autoGate bool
		want                    tools.PermissionMode
	}{
		{name: "default prompt", defaultMode: tools.PermissionPrompt, autoGate: true, want: tools.PermissionPrompt},
		{name: "default auto", defaultMode: tools.PermissionAuto, autoGate: true, want: tools.PermissionAuto},
		{name: "default always approve", defaultMode: tools.PermissionAlwaysApprove, autoGate: true, want: tools.PermissionAlwaysApprove},
		{name: "session yolo", defaultMode: tools.PermissionPrompt, yoloMode: testBoolPointer(true), autoMode: testBoolPointer(true), autoGate: true, want: tools.PermissionAlwaysApprove},
		{name: "disable default yolo", defaultMode: tools.PermissionAlwaysApprove, yoloMode: testBoolPointer(false), autoGate: true, want: tools.PermissionPrompt},
		{name: "session auto", defaultMode: tools.PermissionPrompt, autoMode: testBoolPointer(true), autoGate: true, want: tools.PermissionAuto},
		{name: "disable default auto", defaultMode: tools.PermissionAuto, autoMode: testBoolPointer(false), autoGate: true, want: tools.PermissionPrompt},
		{name: "auto gate", defaultMode: tools.PermissionPrompt, autoMode: testBoolPointer(true), want: tools.PermissionPrompt},
		{name: "managed bypass lock", defaultMode: tools.PermissionPrompt, yoloMode: testBoolPointer(true), disableBypass: true, autoGate: true, want: tools.PermissionPrompt},
		{name: "deny remains locked", defaultMode: tools.PermissionDeny, yoloMode: testBoolPointer(true), autoMode: testBoolPointer(true), autoGate: true, want: tools.PermissionDeny},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := resolveACPSessionPermissionMode(test.defaultMode, test.yoloMode, test.autoMode, test.disableBypass, test.autoGate)
			if got != test.want {
				t.Fatalf("got %q, want %q", got, test.want)
			}
		})
	}
}

func TestResolvePermissionModePrecedenceAndGates(t *testing.T) {
	disabled := false
	for _, test := range []struct {
		name      string
		cfg       config.Config
		cli       string
		cliSet    bool
		want      tools.PermissionMode
		wantError bool
	}{
		{name: "config auto", cfg: config.Config{UI: config.UIConfig{PermissionMode: "auto"}}, want: tools.PermissionAuto},
		{name: "explicit prompt", cfg: config.Config{UI: config.UIConfig{PermissionMode: "auto"}}, cli: "prompt", cliSet: true, want: tools.PermissionPrompt},
		{name: "explicit deny", cfg: config.Config{UI: config.UIConfig{PermissionMode: "always-approve"}}, cli: "deny", cliSet: true, want: tools.PermissionDeny},
		{name: "default is ask", cfg: config.Config{UI: config.UIConfig{PermissionMode: "default"}}, want: tools.PermissionPrompt},
		{name: "managed always gate", cfg: config.Config{UI: config.UIConfig{PermissionMode: "always-approve"}, DisableBypassPermissionsMode: true}, want: tools.PermissionPrompt},
		{name: "auto feature gate", cfg: config.Config{UI: config.UIConfig{PermissionMode: "auto"}, AutoMode: config.AutoModeConfig{Enabled: &disabled}}, want: tools.PermissionPrompt},
		{name: "invalid CLI", cfg: config.Config{UI: config.UIConfig{PermissionMode: "ask"}}, cli: "invalid", cliSet: true, wantError: true},
		{name: "deny cannot come from config", cfg: config.Config{UI: config.UIConfig{PermissionMode: "deny"}}, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := resolvePermissionMode(test.cfg, test.cli, test.cliSet)
			if (err != nil) != test.wantError || got != test.want {
				t.Fatalf("mode=%q err=%v want=%q wantError=%v", got, err, test.want, test.wantError)
			}
		})
	}
}

func testBoolPointer(value bool) *bool { return &value }

func TestResolveACPSessionModel(t *testing.T) {
	cfg := config.Config{
		Model: "default-model", BaseURL: "https://default.example", Backend: "responses", ContextWindow: 1000,
		ModelProfiles: map[string]config.ModelProfile{
			"fast": {Model: "fast-model", BaseURL: "https://fast.example", Backend: "chat", ContextWindow: 2000},
		},
	}
	for _, test := range []struct {
		name, requested, wantModel, wantBaseURL, wantBackend string
		wantContextWindow                                    int
	}{
		{name: "default", wantModel: "default-model", wantBaseURL: "https://default.example", wantBackend: "responses", wantContextWindow: 1000},
		{name: "profile slug", requested: "fast", wantModel: "fast-model", wantBaseURL: "https://fast.example", wantBackend: "chat", wantContextWindow: 2000},
		{name: "profile model ID", requested: "fast-model", wantModel: "fast-model", wantBaseURL: "https://fast.example", wantBackend: "chat", wantContextWindow: 2000},
		{name: "unknown falls back", requested: "unknown", wantModel: "default-model", wantBaseURL: "https://default.example", wantBackend: "responses", wantContextWindow: 1000},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, got := resolveACPSessionModelEntry(cfg, test.requested, false)
			if got.Model != test.wantModel || got.BaseURL != test.wantBaseURL || got.Backend != test.wantBackend || got.ContextWindow != test.wantContextWindow {
				t.Fatalf("resolved config=%#v", got)
			}
		})
	}
}

func TestACPModelOptions(t *testing.T) {
	threshold := 70
	cfg := config.Config{
		Model: "default-model",
		ModelProfiles: map[string]config.ModelProfile{
			"fast":  {Model: "shared-model", Name: "Fast", Description: "Low latency", ContextWindow: 2000, AutoCompactThresholdPercent: &threshold, SupportsReasoningEffort: true, ReasoningEffort: "high"},
			"quick": {Model: "shared-model"},
			"smart": {Model: "smart-model"},
		},
		AllowedModels: []string{"fast", "smart", "default-model"}, HiddenModels: []string{"smart"},
	}
	got := acpModelOptions(cfg)
	want := []agent.ModelOption{
		{ID: "fast", Model: "shared-model", Name: "Fast", Description: "Low latency", ContextWindow: 2000, ReasoningEffort: "high", SupportsReasoningEffort: true, ReasoningEfforts: []agent.ReasoningEffortOption{}},
		{ID: "quick", Model: "shared-model", Name: "shared-model", Disallowed: true, ReasoningEfforts: []agent.ReasoningEffortOption{}},
		{ID: "smart", Model: "smart-model", Name: "smart-model", Hidden: true, ReasoningEfforts: []agent.ReasoningEffortOption{}},
		{ID: "default-model", Model: "default-model", Name: "default-model", ReasoningEfforts: []agent.ReasoningEffortOption{}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("options=%#v want=%#v", got, want)
	}
}

func TestACPSessionModelID(t *testing.T) {
	cfg := config.Config{
		Model: "shared", DefaultModelID: "default",
		ModelProfiles: map[string]config.ModelProfile{
			"default": {Model: "shared"},
			"other":   {Model: "shared"},
		},
	}
	if got := acpSessionModelID(cfg, ""); got != "default" {
		t.Fatalf("default id=%q", got)
	}
	if got := acpSessionModelID(cfg, "other"); got != "other" {
		t.Fatalf("requested id=%q", got)
	}
	if got := acpSessionModelID(cfg, "missing"); got != "default" {
		t.Fatalf("fallback id=%q", got)
	}
}

func TestACPDefaultModelFallsBackToSelectableCatalogEntry(t *testing.T) {
	for _, test := range []struct {
		name          string
		allowed       []string
		hidden        []string
		disabled      []string
		explicitID    string
		explicitModel string
	}{
		{name: "allowlist", allowed: []string{"fast"}, explicitID: "fast", explicitModel: "fast-api"},
		{name: "hidden", hidden: []string{"default"}, explicitID: "default", explicitModel: "default-api"},
		{name: "disabled", disabled: []string{"default"}, explicitID: "fast", explicitModel: "fast-api"},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := config.Config{
				Model: "default-api", DefaultModelID: "default",
				AllowedModels: test.allowed, HiddenModels: test.hidden, DisabledModels: test.disabled,
				ModelProfiles: map[string]config.ModelProfile{
					"default": {Model: "default-api"},
					"fast":    {Model: "fast-api", ContextWindow: 2000},
				},
			}
			id, resolved := resolveACPSessionModelEntry(cfg, "", false)
			if id != "fast" || resolved.Model != "fast-api" || resolved.ContextWindow != 2000 {
				t.Fatalf("id=%q resolved=%#v", id, resolved)
			}
			if explicitID, explicit := resolveACPSessionModelEntry(cfg, "default", false); explicitID != test.explicitID || explicit.Model != test.explicitModel {
				t.Fatalf("explicit id=%q resolved=%#v", explicitID, explicit)
			}
			if restoredID, restored := resolveACPSessionModelEntry(cfg, "default", true); restoredID != "fast" || restored.Model != "fast-api" {
				t.Fatalf("restored id=%q resolved=%#v", restoredID, restored)
			}
		})
	}
}
