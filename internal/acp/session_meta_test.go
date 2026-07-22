package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lookcorner/go-cli/internal/agent"
	sessionlog "github.com/lookcorner/go-cli/internal/session"
	"github.com/lookcorner/go-cli/internal/tools"
	"github.com/lookcorner/go-cli/internal/workspace"
)

func TestSessionConfigOptionsMatchModelState(t *testing.T) {
	runner := &agent.Runner{
		ModelID: "fast", ReasoningEffort: "high",
		ModelOptions: []agent.ModelOption{
			{ID: "fast", Name: "Fast", SupportsReasoningEffort: true, ReasoningEfforts: []agent.ReasoningEffortOption{
				{ID: "low", Value: "low", Label: "Low"},
				{ID: "high", Value: "high", Label: "High", Description: "More reasoning", Default: true},
			}},
			{ID: "smart"},
			{ID: "hidden", Name: "Hidden", Hidden: true},
		},
	}
	options := sessionConfigOptions(runner, modelState(runner))
	if len(options) != 4 || options[0].ID != "fast" || !options[0].Selected || options[1].ID != "smart" || options[1].Label != "smart" || options[2].Category != "mode" || options[2].Selected || options[3].ID != "high" || !options[3].Selected || options[3].Description != "More reasoning" {
		t.Fatalf("session options=%#v", options)
	}
	runner.ReasoningEffort = ""
	if defaults := sessionConfigOptions(runner, modelState(runner)); !defaults[3].Selected {
		t.Fatalf("default reasoning effort was not selected: %#v", defaults)
	}
	runner.ReasoningEffort = "high"

	runner.ModelOptions[0].ReasoningEfforts = nil
	legacy := sessionConfigOptions(runner, modelState(runner))
	if len(legacy) != 7 || legacy[2].ID != "minimal" || legacy[6].ID != "xhigh" || legacy[6].Label != "X-High" {
		t.Fatalf("legacy session options=%#v", legacy)
	}
	runner.ModelOptions[0].SupportsReasoningEffort = false
	if withoutModes := sessionConfigOptions(runner, modelState(runner)); len(withoutModes) != 2 {
		t.Fatalf("unsupported model exposed reasoning modes: %#v", withoutModes)
	}
}

func TestSessionStartResponseMetadata(t *testing.T) {
	current := &session{id: "detail-session", cwd: "/work/project", title: "Stored title", runner: &agent.Runner{
		ModelID: "grok-build", ModelOptions: []agent.ModelOption{{ID: "grok-build", Name: "Grok Build"}},
	}}
	response := sessionStartResponse(current, "plan")
	meta := response["_meta"].(map[string]any)
	config := meta["x.ai/sessionConfig"].(map[string]any)
	detail := meta["x.ai/sessionDetail"].(sessionDetail)
	if response["sessionId"] != current.id || len(config["options"].([]sessionConfigOption)) != 1 || detail.SessionID != current.id || detail.Kind != "build" || detail.CWD != current.cwd || detail.CurrentModelID != "grok-build" || detail.Title != "Stored title" {
		t.Fatalf("session response=%#v", response)
	}
	encoded, err := json.Marshal(detail)
	if err != nil || !bytes.Contains(encoded, []byte(`"currentModelId":"grok-build"`)) || !bytes.Contains(encoded, []byte(`"title":"Stored title"`)) {
		t.Fatalf("session detail JSON=%s err=%v", encoded, err)
	}
}

func TestRestoreSessionReturnsStoredSessionMetadata(t *testing.T) {
	dir, cwd := t.TempDir(), t.TempDir()
	runACPGit(t, cwd, "init", "-q")
	runACPGit(t, cwd, "config", "user.name", "Fixture")
	runACPGit(t, cwd, "config", "user.email", "fixture@example.invalid")
	if err := os.WriteFile(filepath.Join(cwd, "tracked.txt"), []byte("first\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runACPGit(t, cwd, "add", "tracked.txt")
	runACPGit(t, cwd, "commit", "-qm", "first")
	historicalHead := strings.TrimSpace(runACPGitOutput(t, cwd, "rev-parse", "HEAD"))
	branch := strings.TrimSpace(runACPGitOutput(t, cwd, "branch", "--show-current"))
	if err := os.WriteFile(filepath.Join(cwd, "tracked.txt"), []byte("second\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runACPGit(t, cwd, "commit", "-qam", "second")
	currentHead := strings.TrimSpace(runACPGitOutput(t, cwd, "rev-parse", "HEAD"))
	logger, err := sessionlog.NewLoggerWithID(dir, "stored-metadata")
	if err != nil {
		t.Fatal(err)
	}
	if err := logger.Append("session_metadata", map[string]any{"cwd": cwd, "modelId": "grok-build", "headCommit": historicalHead, "headBranch": branch}); err != nil {
		t.Fatal(err)
	}
	if err := logger.Append("session_title", map[string]any{"title": "Restored title"}); err != nil {
		t.Fatal(err)
	}
	if err := logger.AppendPrompt("hello", nil); err != nil {
		t.Fatal(err)
	}
	if err := logger.Append("model_response", map[string]any{"response_id": "response-1", "text": "hi", "tool_call_count": 0}); err != nil {
		t.Fatal(err)
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	server := &Server{SessionDir: dir, output: &output, sessions: make(map[string]*session), Factory: func(_ context.Context, cfg SessionConfig, approver tools.Approver, _, _ io.Writer) (*agent.Runner, func(), error) {
		ws, err := workspace.Open(cfg.CWD)
		if err != nil {
			return nil, nil, err
		}
		registry := tools.NewRegistry(ws, approver)
		return &agent.Runner{Tools: registry, ModelID: "grok-build", ModelOptions: []agent.ModelOption{{ID: "grok-build", Name: "Grok Build"}}}, func() { _ = registry.Close() }, nil
	}}
	server.handleRestoreSession(context.Background(), message{
		ID: json.RawMessage("1"), Params: json.RawMessage(`{"sessionId":"stored-metadata","cwd":` + quoteJSON(cwd) + `,"mcpServers":[]}`),
	}, false)
	response := decodeACP(t, json.NewDecoder(&output))
	result, ok := response["result"].(map[string]any)
	if !ok {
		t.Fatalf("restore response=%#v", response)
	}
	meta := result["_meta"].(map[string]any)
	detail := meta["x.ai/sessionDetail"].(map[string]any)
	if detail["title"] != "Restored title" || detail["sessionId"] != "stored-metadata" || len(meta["x.ai/sessionConfig"].(map[string]any)["options"].([]any)) != 1 {
		t.Fatalf("restored metadata=%#v", meta)
	}
	divergence := meta["gitDivergence"].(map[string]any)
	if divergence["sessionCommit"] != historicalHead || divergence["currentCommit"] != currentHead || divergence["sessionBranch"] != branch {
		t.Fatalf("git divergence=%#v", divergence)
	}
	if !server.closeSession("stored-metadata") {
		t.Fatal("restored session did not close")
	}
}
