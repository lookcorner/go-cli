package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/api"
	sessionlog "github.com/lookcorner/go-cli/internal/session"
	"github.com/lookcorner/go-cli/internal/tools"
)

func TestModelState(t *testing.T) {
	got := modelState(&agent.Runner{
		ModelID: "fast", Model: "fast-model", ReasoningEffort: "xhigh",
		ModelOptions: []agent.ModelOption{{
			ID: "fast", Name: "Fast", Description: "Fast model", ContextWindow: 2000,
			SupportsReasoningEffort: true, ReasoningEffort: "medium",
			ReasoningEfforts: []agent.ReasoningEffortOption{{ID: "low", Value: "low", Label: "Low"}, {ID: "max", Value: "xhigh", Label: "Max", Default: true}},
		}, {ID: "smart", Name: "Smart"}},
	})
	if got.CurrentModelID != "fast" || len(got.Available) != 2 || got.Available[1].ModelID != "smart" || got.Available[1].Name != "Smart" {
		t.Fatalf("state=%#v", got)
	}
	meta := got.Available[0].Meta
	if got.Available[0].Description != "Fast model" || meta["totalContextTokens"] != 2000 || meta["supportsReasoningEffort"] != true || meta["reasoningEffort"] != "xhigh" {
		t.Fatalf("model metadata=%#v", got.Available[0])
	}
	efforts := meta["reasoningEfforts"].([]map[string]any)
	if len(efforts) != 2 || efforts[1]["id"] != "max" || efforts[1]["value"] != "xhigh" || efforts[1]["default"] != true {
		t.Fatalf("reasoning efforts=%#v", efforts)
	}
	fallback := modelState(&agent.Runner{Model: "fallback"})
	if fallback.CurrentModelID != "fallback" || len(fallback.Available) != 1 || fallback.Available[0].ModelID != "fallback" {
		t.Fatalf("fallback=%#v", fallback)
	}
	if empty := modelState(nil); empty.CurrentModelID != "" || len(empty.Available) != 0 {
		t.Fatalf("empty=%#v", empty)
	}
}

func TestSetSessionModelPersistsAndNotifiesBeforeResponse(t *testing.T) {
	dir := t.TempDir()
	logger, err := sessionlog.NewLoggerWithID(dir, "switch-model")
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	for _, event := range []struct {
		kind string
		data any
	}{
		{"session_metadata", map[string]any{"cwd": "/workspace", "modelId": "old"}},
		{"user_prompt", map[string]any{"text": "before"}},
		{"model_response", map[string]any{"response_id": "old-response", "text": "answer", "tool_call_count": 0}},
	} {
		if err := logger.Append(event.kind, event.data); err != nil {
			t.Fatal(err)
		}
	}
	oldClient, newClient := &fixtureStreamer{}, &fixtureStreamer{}
	changed := false
	runner := &agent.Runner{
		Client: oldClient, Logger: logger, ModelID: "old", Model: "old-model", ReasoningEffort: "low",
		ModelOptions: []agent.ModelOption{{ID: "old", Name: "Old"}, {ID: "new", Name: "New"}},
		ResolveModel: func(id string) (agent.ModelRuntime, error) {
			return agent.ModelRuntime{ID: id, Client: newClient, Model: "new-model", ContextWindow: 2000, CompactThresholdPercent: 70, ReasoningEffort: "medium", SupportsReasoningEffort: true}, nil
		},
		OnModelChanged: func(agent.ModelRuntime) { changed = true },
	}
	current := &session{id: "switch-model", runner: runner, logPath: logger.Path(), previous: "old-response"}
	var output bytes.Buffer
	server := &Server{output: &output, sessions: map[string]*session{current.id: current}}
	server.handleSetSessionModel(message{ID: json.RawMessage("7"), Params: json.RawMessage(`{"sessionId":"switch-model","modelId":"new","_meta":{"reasoningEffort":"max"}}`)})

	messages := decodeACPOutput(t, output.Bytes())
	if len(messages) != 2 || messages[0]["method"] != "x.ai/session_notification" || messages[1]["id"] != float64(7) {
		t.Fatalf("wire order=%#v", messages)
	}
	params := messages[0]["params"].(map[string]any)
	update := params["update"].(map[string]any)
	if params["sessionId"] != current.id || update["sessionUpdate"] != "model_changed" || update["model_id"] != "new" || update["reasoning_effort"] != "xhigh" {
		t.Fatalf("notification=%#v", messages[0])
	}
	if _, exists := params["_meta"]; exists {
		t.Fatalf("broadcast-only notification has replay metadata=%#v", params)
	}
	meta := messages[1]["result"].(map[string]any)["_meta"].(map[string]any)
	if meta["model"] != "new-model" || !changed || runner.Client != newClient || runner.ModelID != "new" || runner.ReasoningEffort != "xhigh" || current.previous != "" {
		t.Fatalf("result=%#v runner=%#v previous=%q", messages[1], runner, current.previous)
	}
	items, err := sessionlog.List(dir, "/workspace")
	if err != nil || len(items) != 1 || items[0].ModelID != "new" {
		t.Fatalf("persisted model=%#v err=%v", items, err)
	}
	if items[0].ReasoningEffort != "xhigh" {
		t.Fatalf("persisted reasoning effort=%#v", items[0])
	}
	events, err := sessionlog.Events(logger.Path(), "xai_session_notification")
	if err != nil || len(events) != 0 {
		t.Fatalf("model_changed was persisted: events=%#v err=%v", events, err)
	}
}

func TestSetSessionModelIgnoresUnsupportedOrInvalidReasoningEffort(t *testing.T) {
	for _, test := range []struct {
		name, effort string
		supported    bool
		want         string
	}{
		{name: "unsupported", effort: "high"},
		{name: "invalid", effort: "ultra", supported: true, want: "low"},
	} {
		t.Run(test.name, func(t *testing.T) {
			logger, err := sessionlog.NewLoggerWithID(t.TempDir(), "effort")
			if err != nil {
				t.Fatal(err)
			}
			defer logger.Close()
			if err := logger.Append("session_metadata", map[string]any{"cwd": "/workspace", "modelId": "old"}); err != nil {
				t.Fatal(err)
			}
			runner := &agent.Runner{
				Client: &fixtureStreamer{}, Logger: logger, ModelID: "old", Model: "old",
				ModelOptions: []agent.ModelOption{{ID: "new", Name: "New"}},
				ResolveModel: func(string) (agent.ModelRuntime, error) {
					return agent.ModelRuntime{ID: "new", Client: &fixtureStreamer{}, Model: "new", ReasoningEffort: "low", SupportsReasoningEffort: test.supported}, nil
				},
			}
			current := &session{id: "effort", runner: runner, logPath: logger.Path()}
			var output bytes.Buffer
			server := &Server{output: &output, sessions: map[string]*session{current.id: current}}
			params, _ := json.Marshal(map[string]any{"sessionId": current.id, "modelId": "new", "_meta": map[string]any{"reasoningEffort": test.effort}})
			server.handleSetSessionModel(message{ID: json.RawMessage("1"), Params: params})
			if runner.ReasoningEffort != test.want {
				t.Fatalf("reasoning effort=%q", runner.ReasoningEffort)
			}
		})
	}
}

func TestSetSessionModelBeforeFirstTurn(t *testing.T) {
	logger, err := sessionlog.NewLoggerWithID(t.TempDir(), "new-session")
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	if err := logger.Append("session_metadata", map[string]any{"cwd": "/workspace", "modelId": "old"}); err != nil {
		t.Fatal(err)
	}
	newClient := &fixtureStreamer{}
	runner := &agent.Runner{
		Client: &fixtureStreamer{}, Logger: logger, ModelID: "old", Model: "old",
		ModelOptions: []agent.ModelOption{{ID: "new", Name: "New"}},
		ResolveModel: func(string) (agent.ModelRuntime, error) {
			return agent.ModelRuntime{ID: "new", Client: newClient, Model: "new"}, nil
		},
	}
	current := &session{id: "new-session", runner: runner, logPath: logger.Path()}
	var output bytes.Buffer
	server := &Server{output: &output, sessions: map[string]*session{current.id: current}}
	server.handleSetSessionModel(message{ID: json.RawMessage("1"), Params: json.RawMessage(`{"sessionId":"new-session","modelId":"new"}`)})
	if messages := decodeACPOutput(t, output.Bytes()); len(messages) != 2 || messages[1]["error"] != nil {
		t.Fatalf("messages=%#v", messages)
	}
	if runner.Client != newClient || runner.ModelID != "new" {
		t.Fatalf("runner=%#v", runner)
	}
}

func TestRestoredModelSwitchSeedsFreshResponseChain(t *testing.T) {
	sessionDir, root := t.TempDir(), t.TempDir()
	logger, err := sessionlog.NewLoggerWithID(sessionDir, "restored-switch")
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []struct {
		kind string
		data any
	}{
		{"session_metadata", map[string]any{"cwd": root, "modelId": "old"}},
		{"user_prompt", map[string]any{"text": "before"}},
		{"model_response", map[string]any{"response_id": "old-response", "text": "answer", "tool_call_count": 0}},
		{"session_model", map[string]any{"model_id": "new"}},
	} {
		if err := logger.Append(event.kind, event.data); err != nil {
			t.Fatal(err)
		}
	}
	path := logger.Path()
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	streamer := &fixtureStreamer{results: []api.StreamResult{{ResponseID: "new-response", Text: "continued"}}}
	var output bytes.Buffer
	server := &Server{
		SessionDir: sessionDir, output: &output, sessions: make(map[string]*session),
		Factory: func(_ context.Context, cfg SessionConfig, _ tools.Approver, _, _ io.Writer) (*agent.Runner, func(), error) {
			resumed, _, err := sessionlog.Resume(cfg.ResumePath)
			if err != nil {
				return nil, nil, err
			}
			registry := permissionRegistry(t, tools.PermissionAuto)
			return &agent.Runner{Client: streamer, Tools: registry, Logger: resumed, Model: "new", MaxSteps: 1}, func() {
				_ = resumed.Close()
				_ = registry.Close()
			}, nil
		},
	}
	created, err := server.startSession(context.Background(), "restored-switch", SessionConfig{CWD: root, ResumePath: path, Model: "new"}, "")
	if err != nil {
		t.Fatal(err)
	}
	defer created.close()
	params, _ := json.Marshal(map[string]any{"sessionId": created.id, "prompt": []any{map[string]any{"type": "text", "text": "current"}}})
	server.handlePrompt(context.Background(), message{ID: json.RawMessage("1"), Params: params})
	server.wg.Wait()
	streamer.mu.Lock()
	requests := append([]api.ResponseRequest(nil), streamer.requests...)
	streamer.mu.Unlock()
	if len(requests) != 1 || requests[0].PreviousResponseID != "" || len(requests[0].Input) != 3 {
		t.Fatalf("requests=%#v", requests)
	}
	if requests[0].Input[0].Role != "user" || requests[0].Input[1].Role != "assistant" || requests[0].Input[2].Role != "user" {
		t.Fatalf("history order=%#v", requests[0].Input)
	}
	responseID, err := sessionlog.CompletedResponseID(path)
	if err != nil || responseID != "new-response" {
		t.Fatalf("new response chain=%q err=%v", responseID, err)
	}
}

func TestSetSessionModelRejectsInvalidBusyAndResolverFailure(t *testing.T) {
	newRunner := func() *agent.Runner {
		return &agent.Runner{
			Client: &fixtureStreamer{}, ModelID: "old", Model: "old-model",
			ModelOptions: []agent.ModelOption{{ID: "new", Name: "New"}},
			ResolveModel: func(string) (agent.ModelRuntime, error) {
				return agent.ModelRuntime{}, errors.New("client unavailable")
			},
		}
	}
	for _, test := range []struct {
		name   string
		model  string
		mutate func(*session)
		code   float64
	}{
		{name: "unknown model", model: "missing", code: -32602},
		{name: "running", model: "new", code: -32000, mutate: func(current *session) { current.running = true }},
		{name: "starting prompt", model: "new", code: -32000, mutate: func(current *session) { current.startingPromptID = "queued" }},
		{name: "recap", model: "new", code: -32000, mutate: func(current *session) { current.recapDone = make(chan struct{}) }},
		{name: "resolver failure", model: "new", code: -32000},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			logger, err := sessionlog.NewLoggerWithID(dir, "failure")
			if err != nil {
				t.Fatal(err)
			}
			defer logger.Close()
			if err := logger.Append("session_metadata", map[string]any{"cwd": "/workspace", "modelId": "old"}); err != nil {
				t.Fatal(err)
			}
			if err := logger.Append("user_prompt", map[string]any{"text": "before"}); err != nil {
				t.Fatal(err)
			}
			if err := logger.Append("model_response", map[string]any{"response_id": "old-response", "text": "answer", "tool_call_count": 0}); err != nil {
				t.Fatal(err)
			}
			runner := newRunner()
			runner.Logger = logger
			current := &session{id: "failure", runner: runner, logPath: logger.Path(), previous: "old-response"}
			if test.mutate != nil {
				test.mutate(current)
			}
			var output bytes.Buffer
			server := &Server{output: &output, sessions: map[string]*session{current.id: current}}
			params, _ := json.Marshal(map[string]any{"sessionId": current.id, "modelId": test.model})
			server.handleSetSessionModel(message{ID: json.RawMessage("1"), Params: params})
			messages := decodeACPOutput(t, output.Bytes())
			if len(messages) != 1 || messages[0]["error"].(map[string]any)["code"] != test.code {
				t.Fatalf("messages=%#v", messages)
			}
			if runner.ModelID != "old" || runner.Model != "old-model" || current.previous != "old-response" {
				t.Fatalf("failed switch mutated state: runner=%#v previous=%q", runner, current.previous)
			}
			events, err := sessionlog.Events(logger.Path(), "session_model")
			if err != nil || len(events) != 0 {
				t.Fatalf("failed switch persisted events=%#v err=%v", events, err)
			}
		})
	}
}
