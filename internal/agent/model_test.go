package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lookcorner/go-cli/internal/session"
)

func TestRunnerSwitchModelResolvesNameEffortAndRestoresHistory(t *testing.T) {
	dir := t.TempDir()
	logger, err := session.NewLoggerWithID(dir, "model-switch")
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	if err := logger.AppendPrompt("hello", nil); err != nil {
		t.Fatal(err)
	}
	if err := logger.Append("model_response", map[string]any{"response_id": "old-response", "text": "hi", "tool_call_count": 0}); err != nil {
		t.Fatal(err)
	}
	streamer := &rewindingModelStreamer{}
	changed := false
	persisted := ""
	runner := &Runner{
		Logger: logger, SessionPath: logger.Path(), ModelID: "old", Model: "old-api",
		ModelOptions: []ModelOption{
			{ID: "reasoning", Model: "reasoning-api", Name: "Reasoning X", SupportsReasoningEffort: true, ReasoningEfforts: []ReasoningEffortOption{{ID: "low", Value: "low"}, {ID: "max", Value: "xhigh"}}},
			{ID: "hidden", Name: "Hidden", Hidden: true},
			{ID: "blocked", Name: "Blocked", Disallowed: true},
		},
		ResolveModel: func(id string) (ModelRuntime, error) {
			return ModelRuntime{ID: id, Client: streamer, Model: "reasoning-api", ContextWindow: 2000, CompactThresholdPercent: 75, ReasoningEffort: "low", SupportsReasoningEffort: true}, nil
		},
		SetDefaultModel: func(id string) error { persisted = id; return nil },
		OnModelChanged:  func(ModelRuntime) { changed = true },
	}
	option, err := runner.SwitchModelCommand("Reasoning X max")
	if err != nil {
		t.Fatal(err)
	}
	if option.ID != "reasoning" || option.ReasoningEffort != "xhigh" || runner.ModelID != "reasoning" || runner.Model != "reasoning-api" || runner.ReasoningEffort != "xhigh" || runner.ContextWindow != 2000 || !changed {
		t.Fatalf("option=%#v runner=%#v changed=%v", option, runner, changed)
	}
	if len(streamer.history) != 2 || streamer.history[0].Text != "hello" || streamer.history[1].Text != "hi" {
		t.Fatalf("history=%#v", streamer.history)
	}
	if persisted != "" {
		t.Fatalf("model+effort unexpectedly persisted default=%q", persisted)
	}
	if _, err := runner.SwitchModelCommand("Reasoning X"); err != nil {
		t.Fatal(err)
	}
	if persisted != "reasoning" || runner.ReasoningEffort != "low" {
		t.Fatalf("persisted=%q effort=%q", persisted, runner.ReasoningEffort)
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "model-switch.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !containsAll(string(data), `"kind":"session_model"`, `"model_id":"reasoning"`, `"reasoning_effort":"xhigh"`) {
		t.Fatalf("session log=%s", data)
	}
}

func TestRunnerModelSelectionFiltersAndValidates(t *testing.T) {
	runner := &Runner{ModelID: "plain", ModelOptions: []ModelOption{
		{ID: "plain", Model: "plain-api", Name: "Plain"},
		{ID: "reasoning", Name: "Reasoning", SupportsReasoningEffort: true},
		{ID: "hidden", Name: "Hidden", Hidden: true},
		{ID: "blocked", Name: "Blocked", Disallowed: true},
	}}
	models := runner.AvailableModels()
	if len(models) != 2 || models[0].ID != "plain" || models[1].ID != "reasoning" {
		t.Fatalf("models=%#v", models)
	}
	if efforts := runner.CurrentReasoningEfforts(); len(efforts) != 0 {
		t.Fatalf("plain efforts=%#v", efforts)
	}
	for _, test := range []struct {
		name, query, effort, want string
	}{
		{"hidden", "hidden", "", "unknown or unavailable"},
		{"blocked", "blocked", "", "unknown or unavailable"},
		{"plain effort", "plain", "high", "does not support"},
		{"bad effort", "reasoning", "turbo", "unknown effort level"},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner.ResolveModel = func(string) (ModelRuntime, error) {
				return ModelRuntime{Client: &fakeStreamer{}, ID: test.query, Model: test.query}, nil
			}
			if _, err := runner.SwitchModel(test.query, test.effort); err == nil || !containsAll(err.Error(), test.want) {
				t.Fatalf("error=%v", err)
			}
		})
	}
	runner.ModelID = "reasoning"
	if efforts := runner.CurrentReasoningEfforts(); len(efforts) != 4 || efforts[0].ID != "xhigh" || efforts[3].ID != "low" {
		t.Fatalf("fallback efforts=%#v", efforts)
	}
	runner.btwRunning.Store(true)
	if _, err := runner.SwitchModel("reasoning", "high"); err == nil || !strings.Contains(err.Error(), "model request is running") {
		t.Fatalf("busy switch error=%v", err)
	}
}

func containsAll(value string, fragments ...string) bool {
	for _, fragment := range fragments {
		if !strings.Contains(value, fragment) {
			return false
		}
	}
	return true
}
