package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	sessionlog "github.com/lookcorner/go-cli/internal/session"
)

func TestRosterUsesDisplayCWDWithoutChangingExecutionCWD(t *testing.T) {
	var output bytes.Buffer
	server := &Server{output: &output}
	current := &session{id: "display-session", cwd: "/real/worktree", displayCWD: "/project", updated: time.Now().UTC()}
	server.notifyRosterUpsert(current, "idle")
	messages := decodeACPOutput(t, output.Bytes())
	if len(messages) != 1 {
		t.Fatalf("messages=%#v", messages)
	}
	message := messages[0]
	params := message["params"].(map[string]any)
	entry := params["upserted"].([]any)[0].(map[string]any)
	if entry["cwd"] != "/project" {
		t.Fatalf("roster entry=%#v", entry)
	}
	if current.cwd != "/real/worktree" {
		t.Fatalf("execution cwd changed: %q", current.cwd)
	}
}

func TestDormantRosterUsesPersistedDisplayCWD(t *testing.T) {
	dir := t.TempDir()
	logger, err := sessionlog.NewLoggerWithID(dir, "dormant-display")
	if err != nil {
		t.Fatal(err)
	}
	if err := logger.Append("session_metadata", map[string]any{"cwd": "/real/worktree", "displayCwd": "/project"}); err != nil {
		t.Fatal(err)
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	server := &Server{SessionDir: dir, output: &output}
	server.handleSessionRoster(context.Background(), message{ID: json.RawMessage("1"), Method: "x.ai/sessions/list", Params: json.RawMessage(`{}`)})
	var response map[string]any
	if err := json.NewDecoder(&output).Decode(&response); err != nil {
		t.Fatal(err)
	}
	rows := response["result"].(map[string]any)["result"].(map[string]any)["sessions"].([]any)
	if len(rows) != 1 || rows[0].(map[string]any)["cwd"] != "/project" {
		t.Fatalf("rows=%#v", rows)
	}
}

func TestStringMetaTrimsOnlyStringValues(t *testing.T) {
	meta := map[string]any{"x.ai/display_cwd": "  /project  ", "wrong": 1}
	if got := stringMeta(meta, "x.ai/display_cwd"); got != "/project" || stringMeta(meta, "wrong") != "" {
		t.Fatalf("display metadata=%q", got)
	}
}
