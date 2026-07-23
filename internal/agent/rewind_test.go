package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/lookcorner/go-cli/internal/api"
	"github.com/lookcorner/go-cli/internal/session"
	"github.com/lookcorner/go-cli/internal/tools"
	"github.com/lookcorner/go-cli/internal/workspace"
)

func TestRunnerRewindCoordinatesConversationFilesAndNextPrompt(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "state.txt")
	if err := os.WriteFile(file, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry(ws, tools.PromptApprover{Mode: tools.PermissionAuto})
	defer registry.Close()
	logger, err := session.NewLoggerWithID(t.TempDir(), "rewind-runner")
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	for index, prompt := range []string{"first request", "second request"} {
		if err := logger.AppendPrompt(prompt, nil); err != nil {
			t.Fatal(err)
		}
		if err := logger.Append("model_response", map[string]any{"response_id": fmt.Sprintf("response-%d", index+1), "text": "done", "tool_call_count": 0}); err != nil {
			t.Fatal(err)
		}
	}
	store, err := workspace.NewRewindStore(ws, filepath.Join(t.TempDir(), "rewind.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CaptureBefore(1, "state.txt"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("second"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.CaptureAfter(1, "state.txt"); err != nil {
		t.Fatal(err)
	}
	streamer := &rewindingModelStreamer{modelSwitchStreamer: modelSwitchStreamer{results: []api.StreamResult{
		{ResponseID: "tool", ToolCalls: []api.ToolCall{{CallID: "call-1", Name: "shell", Arguments: json.RawMessage(`{"command":"printf branch > state.txt"}`)}}},
		{ResponseID: "branched", Text: "done"},
	}}}
	runner := &Runner{Client: streamer, Tools: registry, Logger: logger, SessionPath: logger.Path(), Workspace: root, Model: "test", MaxSteps: 2}
	if err := runner.EnableRewind(store, 2); err != nil {
		t.Fatal(err)
	}
	runner.rewind.active.Store(2)
	if _, err := runner.RewindPoints(); err == nil {
		t.Fatal("rewind points were listed while a turn was active")
	}
	runner.rewind.active.Store(-1)
	points, err := runner.RewindPoints()
	if err != nil || len(points) != 2 || !points[1].HasFileChanges {
		t.Fatalf("points=%#v err=%v", points, err)
	}
	preview, err := runner.PreviewRewind(1, RewindConversationOnly)
	if err != nil || preview.PromptText != "second request" || len(preview.CleanFiles) != 0 {
		t.Fatalf("preview=%#v err=%v", preview, err)
	}
	result, err := runner.ExecuteRewind(1, RewindConversationOnly)
	if err != nil || result.PreviousResponseID != "response-1" || len(streamer.history) != 2 {
		t.Fatalf("result=%#v history=%#v err=%v", result, streamer.history, err)
	}
	if current, _ := os.ReadFile(file); string(current) != "second" {
		t.Fatalf("conversation rewind changed files=%q", current)
	}
	if _, err := runner.RunTurn(context.Background(), "new branch", result.PreviousResponseID); err != nil {
		t.Fatal(err)
	}
	counts, err := store.Counts()
	if err != nil || counts[1] != 1 || counts[2] != 0 {
		t.Fatalf("branched checkpoint counts=%#v err=%v", counts, err)
	}
	if current, _ := os.ReadFile(file); string(current) != "branch" {
		t.Fatalf("branched file=%q", current)
	}
}

func TestRunnerRewindAllRestoresFilesAndRejectsInvalidMode(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "state.txt")
	if err := os.WriteFile(file, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry(ws, tools.PromptApprover{Mode: tools.PermissionAuto})
	defer registry.Close()
	logger, err := session.NewLoggerWithID(t.TempDir(), "rewind-all")
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	if err := logger.AppendPrompt("change it", nil); err != nil {
		t.Fatal(err)
	}
	if err := logger.Append("model_response", map[string]any{"response_id": "response", "text": "done", "tool_call_count": 0}); err != nil {
		t.Fatal(err)
	}
	store, err := workspace.NewRewindStore(ws, filepath.Join(t.TempDir(), "rewind.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CaptureBefore(0, "state.txt"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("after"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.CaptureAfter(0, "state.txt"); err != nil {
		t.Fatal(err)
	}
	runner := &Runner{Tools: registry, SessionPath: logger.Path()}
	if err := runner.EnableRewind(store, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.PreviewRewind(0, RewindMode("invalid")); err == nil {
		t.Fatal("invalid rewind mode was accepted")
	}
	filesOnly, err := runner.ExecuteRewind(0, RewindFilesOnly)
	if err != nil || len(filesOnly.RevertedFiles) != 1 || len(filesOnly.Messages) != 0 {
		t.Fatalf("files-only result=%#v err=%v", filesOnly, err)
	}
	points, err := session.RewindPoints(logger.Path())
	if err != nil || len(points) != 1 {
		t.Fatalf("files-only changed conversation points=%#v err=%v", points, err)
	}
	if err := store.CaptureBefore(0, "state.txt"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("after again"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.CaptureAfter(0, "state.txt"); err != nil {
		t.Fatal(err)
	}
	result, err := runner.ExecuteRewind(0, RewindAll)
	if err != nil || len(result.RevertedFiles) != 1 {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if current, _ := os.ReadFile(file); string(current) != "before" {
		t.Fatalf("restored file=%q", current)
	}
}
