package acp

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	sessionlog "github.com/lookcorner/go-cli/internal/session"
)

func TestPathRewriterHandlesPlainAndEncodedPaths(t *testing.T) {
	rewriter := newPathRewriter("/root/.grok/worktrees/project/overlay", "/home/user/project")
	input := "file=/root/.grok/worktrees/project/overlay/main.go output=%2Froot%2F.grok%2Fworktrees%2Fproject%2Foverlay%2Ftask.log"
	got := rewriter.rewrite(input)
	if strings.Contains(got, "overlay") || !strings.Contains(got, "/home/user/project/main.go") || !strings.Contains(got, "%2Fhome%2Fuser%2Fproject%2Ftask.log") {
		t.Fatalf("rewritten=%q", got)
	}
}

func TestSessionNotificationRewritesStructuredPaths(t *testing.T) {
	const realCWD, displayCWD = "/root/.grok/worktrees/project/overlay", "/project"
	var output bytes.Buffer
	server := &Server{output: &output}
	server.pathRewriters.Store("rewrite-session", newPathRewriter(realCWD, displayCWD))
	server.notify("rewrite-session", map[string]any{
		"sessionUpdate": "tool_call_update",
		"rawInput":      json.RawMessage(`{"path":"` + realCWD + `/main.go"}`),
		"rawOutput": map[string]any{
			"cwd":         realCWD,
			"output_file": "/sessions/%2Froot%2F.grok%2Fworktrees%2Fproject%2Foverlay/task.log",
		},
	})
	messages := decodeACPOutput(t, output.Bytes())
	update := messages[0]["params"].(map[string]any)["update"].(map[string]any)
	encoded, _ := json.Marshal(update)
	if strings.Contains(string(encoded), "overlay") || !strings.Contains(string(encoded), "/project/main.go") || !strings.Contains(string(encoded), "%2Fproject") {
		t.Fatalf("update=%s", encoded)
	}
}

func TestReplayRewritesDisplayPaths(t *testing.T) {
	dir := t.TempDir()
	logger, err := sessionlog.NewLoggerWithID(dir, "rewrite-replay")
	if err != nil {
		t.Fatal(err)
	}
	if err := logger.Append("session_metadata", map[string]any{"cwd": "/real/worktree"}); err != nil {
		t.Fatal(err)
	}
	if err := logger.Append("user_prompt", map[string]any{"text": "inspect /real/worktree/main.go"}); err != nil {
		t.Fatal(err)
	}
	if err := logger.Append("model_response", map[string]any{"text": "found /real/worktree/main.go", "response_id": "r1"}); err != nil {
		t.Fatal(err)
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	server := &Server{output: &output}
	if err := server.replaySessionWithPaths(logger.Path(), logger.ID(), "/real/worktree", "/project"); err != nil {
		t.Fatal(err)
	}
	encoded := output.String()
	if strings.Contains(encoded, "/real/worktree") || strings.Count(encoded, "/project/main.go") != 2 {
		t.Fatalf("replay=%s", encoded)
	}
}
