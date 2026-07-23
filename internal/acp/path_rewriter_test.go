package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lookcorner/go-cli/internal/agent"
	sessionlog "github.com/lookcorner/go-cli/internal/session"
	"github.com/lookcorner/go-cli/internal/tools"
	"github.com/lookcorner/go-cli/internal/workspace"
)

func TestPathRewriterHandlesPlainAndEncodedPaths(t *testing.T) {
	rewriter := newPathRewriter("/root/.grok/worktrees/project/overlay", "/home/user/project")
	input := "file=/root/.grok/worktrees/project/overlay/main.go output=%2Froot%2F.grok%2Fworktrees%2Fproject%2Foverlay%2Ftask.log"
	got := rewriter.rewrite(input)
	if strings.Contains(got, "overlay") || !strings.Contains(got, "/home/user/project/main.go") || !strings.Contains(got, "%2Fhome%2Fuser%2Fproject%2Ftask.log") {
		t.Fatalf("rewritten=%q", got)
	}
}

func TestPathRewriterMapsOnlyPathsInsideRoot(t *testing.T) {
	rewriter := newPathRewriter("/real/worktree", "/project")
	for input, want := range map[string]string{
		"src/main.go":                "/project/src/main.go",
		"/real/worktree/src/main.go": "/project/src/main.go",
		"/real/other/main.go":        "/real/other/main.go",
	} {
		if got := rewriter.rewritePath(input); got != want {
			t.Errorf("rewritePath(%q)=%q want %q", input, got, want)
		}
	}
	if got := newPathRewriter("/project", "/real/worktree").rewritePath("/project/src/main.go"); got != "/real/worktree/src/main.go" {
		t.Fatalf("reverse path=%q", got)
	}
}

func TestDisplayHunkPathUsesDisplayRoot(t *testing.T) {
	for input, want := range map[string]string{
		"src/main.go":                "/project/src/main.go",
		"/real/worktree/src/main.go": "/project/src/main.go",
		"/outside/main.go":           "/outside/main.go",
	} {
		if got := displayHunkPath("/real/worktree", "/project", input); got != want {
			t.Errorf("displayHunkPath(%q)=%q want %q", input, got, want)
		}
	}
}

func TestHunkHandlersRoundTripDisplayPath(t *testing.T) {
	root := t.TempDir()
	runPathRewriteGit(t, root, "init", "-q")
	path := filepath.Join(root, "tracked.txt")
	if err := os.WriteFile(path, []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runPathRewriteGit(t, root, "add", "tracked.txt")
	runPathRewriteGit(t, root, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "baseline")
	if err := os.WriteFile(path, []byte("after\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry(ws, tools.PromptApprover{Mode: tools.PermissionAuto})
	defer registry.Close()
	var output bytes.Buffer
	server := &Server{output: &output, sessions: map[string]*session{"hunk-display": {
		id: "hunk-display", cwd: root, displayCWD: "/project", runner: &agent.Runner{Tools: registry},
	}}}
	server.handleHunkQuery(context.Background(), message{ID: json.RawMessage("1"), Method: "x.ai/hunk-tracker/get-hunks", Params: json.RawMessage(`{"sessionId":"hunk-display","path":"/project/tracked.txt"}`)})
	responses := decodeACPOutput(t, output.Bytes())
	hunks := responses[0]["result"].(map[string]any)["hunks"].([]any)
	if len(hunks) != 1 || hunks[0].(map[string]any)["path"] != "/project/tracked.txt" {
		t.Fatalf("hunks=%#v", hunks)
	}
	output.Reset()
	server.handleHunkAction(context.Background(), message{ID: json.RawMessage("2"), Method: "x.ai/hunk-tracker/file-action", Params: json.RawMessage(`{"sessionId":"hunk-display","path":"/project/tracked.txt","action":"accept"}`)})
	responses = decodeACPOutput(t, output.Bytes())
	if result := responses[0]["result"].(map[string]any); result["success"] != true || result["affectedCount"] != float64(1) {
		t.Fatalf("action=%#v", result)
	}
}

func runPathRewriteGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
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
