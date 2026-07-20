package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/api"
	"github.com/lookcorner/go-cli/internal/tools"
	"github.com/lookcorner/go-cli/internal/workspace"
)

type questionSignalWriter struct {
	once sync.Once
	seen chan struct{}
}

func (w *questionSignalWriter) Write(data []byte) (int, error) {
	if strings.Contains(string(data), "Question ") {
		w.once.Do(func() { close(w.seen) })
	}
	return len(data), nil
}

func TestTerminalQuestionObserverCollectsAnswers(t *testing.T) {
	var output strings.Builder
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	prompter := &terminalPrompter{input: newTerminalInput(ctx, bufio.NewReader(strings.NewReader("1\ncustom deployment\n"))), output: &output}
	response, err := prompter.AskUserQuestion(ctx, tools.UserQuestionRequest{Questions: []tools.UserQuestion{
		{Question: "Database?", Options: []tools.UserQuestionOption{{Label: "SQLite", Description: "Local"}}},
		{Question: "Target?", Options: []tools.UserQuestionOption{{Label: "Cloud", Description: "Remote"}}},
	}})
	if err != nil || response.Outcome != "accepted" || response.Answers["Database?"][0] != "SQLite" || response.Answers["Target?"][0] != "Other" || response.Annotations["Target?"].Notes != "custom deployment" {
		t.Fatalf("response=%#v err=%v", response, err)
	}
	if !strings.Contains(output.String(), "Question 1/2") || !strings.Contains(output.String(), "SQLite - Local") {
		t.Fatalf("output=%q", output.String())
	}
}

func TestTerminalPrompterApprovesThroughInteractionQueue(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var output strings.Builder
	prompter := &terminalPrompter{input: newTerminalInput(ctx, bufio.NewReader(strings.NewReader("yes\n"))), output: &output}
	if err := prompter.Approve(ctx, "shell", "go test ./..."); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Allow shell?") {
		t.Fatalf("output=%q", output.String())
	}
}

func TestTerminalQuestionObserverPlanSkipAndEOF(t *testing.T) {
	request := tools.UserQuestionRequest{Mode: "plan", Questions: []tools.UserQuestion{
		{Question: "First?", Options: []tools.UserQuestionOption{{Label: "A"}}},
		{Question: "Second?", Options: []tools.UserQuestionOption{{Label: "B"}}},
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	prompter := &terminalPrompter{input: newTerminalInput(ctx, bufio.NewReader(strings.NewReader("1\n/skip\n"))), output: &strings.Builder{}}
	response, err := prompter.AskUserQuestion(ctx, request)
	if err != nil || response.Outcome != "skip_interview" || response.PartialAnswers["First?"] != "A" {
		t.Fatalf("response=%#v err=%v", response, err)
	}
	prompter = &terminalPrompter{input: newTerminalInput(ctx, bufio.NewReader(strings.NewReader(""))), output: &strings.Builder{}}
	response, err = prompter.AskUserQuestion(ctx, request)
	if err != nil || response.Outcome != "cancelled" {
		t.Fatalf("EOF response=%#v err=%v", response, err)
	}
}

func TestTerminalInputPrioritizesInteractionAndBroadcastsEOF(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reader, writer := io.Pipe()
	input := newTerminalInput(ctx, bufio.NewReader(reader))
	prompt := input.request(ctx, false)
	interaction := input.request(ctx, true)
	if _, err := io.WriteString(writer, "answer\n"); err != nil {
		t.Fatal(err)
	}
	if got := <-interaction; got.line != "answer\n" || got.err != nil {
		t.Fatalf("interaction=%#v", got)
	}
	select {
	case got := <-prompt:
		t.Fatalf("prompt received interaction answer: %#v", got)
	default:
	}
	if _, err := io.WriteString(writer, "prompt\n"); err != nil {
		t.Fatal(err)
	}
	if got := <-prompt; got.line != "prompt\n" || got.err != nil {
		t.Fatalf("prompt=%#v", got)
	}
	staleCtx, staleCancel := context.WithCancel(ctx)
	stale := input.request(staleCtx, true)
	staleCancel()
	prompt = input.request(ctx, false)
	if _, err := io.WriteString(writer, "live prompt\n"); err != nil {
		t.Fatal(err)
	}
	if got := <-prompt; got.line != "live prompt\n" || got.err != nil {
		t.Fatalf("live prompt=%#v", got)
	}
	if got := <-stale; !errors.Is(got.err, context.Canceled) {
		t.Fatalf("stale interaction=%#v", got)
	}
	prompt, interaction = input.request(ctx, false), input.request(ctx, true)
	_ = writer.Close()
	if got := <-interaction; !errors.Is(got.err, io.EOF) {
		t.Fatalf("interaction EOF=%#v", got)
	}
	if got := <-prompt; !errors.Is(got.err, io.EOF) {
		t.Fatalf("prompt EOF=%#v", got)
	}
}

func TestInteractiveLoopRoutesQuestionAnswers(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry(ws, tools.PromptApprover{Mode: tools.PermissionAuto})
	defer registry.Close()
	streamer := &scheduledStreamer{results: []api.StreamResult{
		{ResponseID: "ask-response", ToolCalls: []api.ToolCall{{CallID: "ask-1", Name: "ask_user_question", Arguments: json.RawMessage(`{"questions":[{"question":"Database?","options":[{"label":"SQLite","description":"Local"}]}]}`)}}},
		{ResponseID: "done-response", Text: "done"},
	}}
	runner := &agent.Runner{Client: streamer, Tools: registry, Model: "test", MaxSteps: 2}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	reader, writer := io.Pipe()
	input := newTerminalInput(ctx, bufio.NewReader(reader))
	questionShown := &questionSignalWriter{seen: make(chan struct{})}
	prompter := &terminalPrompter{input: input, output: questionShown}
	registry.SetUserQuestionObserver(prompter)
	queue := newScheduledWakeQueue()
	event := tools.ScheduledTaskFired{TaskID: "loop-ask", Prompt: "ask the user"}
	queue.ScheduledTaskCreated(event)
	queue.ScheduledTaskFired(event)
	queue.ScheduledTaskRemoved(event.TaskID)
	done := make(chan error, 1)
	go func() {
		done <- interactiveLoop(ctx, runner, queue, input, io.Discard, io.Discard, "", "")
	}()
	select {
	case <-questionShown.seen:
	case <-ctx.Done():
		t.Fatal("scheduled turn did not present its question")
	}
	if _, err := io.WriteString(writer, "1\n/exit\n"); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	_ = writer.Close()
	streamer.mu.Lock()
	defer streamer.mu.Unlock()
	if len(streamer.requests) != 2 {
		t.Fatalf("requests=%#v", streamer.requests)
	}
	encoded, _ := json.Marshal(streamer.requests[1].Input)
	if !strings.Contains(string(encoded), `\"Database?\"=\"SQLite\"`) {
		t.Fatalf("tool answer missing from next step: %s", encoded)
	}
}
