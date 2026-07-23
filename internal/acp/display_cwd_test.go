package acp

import (
	"bytes"
	"testing"
	"time"
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

func TestStringMetaTrimsOnlyStringValues(t *testing.T) {
	meta := map[string]any{"x.ai/display_cwd": "  /project  ", "wrong": 1}
	if got := stringMeta(meta, "x.ai/display_cwd"); got != "/project" || stringMeta(meta, "wrong") != "" {
		t.Fatalf("display metadata=%q", got)
	}
}
