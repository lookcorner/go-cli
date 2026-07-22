package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"testing"

	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/tools"
)

func TestEvictSessionsIgnoresInvalidEmptyAndUnknownParams(t *testing.T) {
	current := &session{id: "known"}
	server := &Server{sessions: map[string]*session{"known": current}}
	for _, raw := range []string{"", "null", `{}`, `{"sessionIds":[]}`, `{"sessionIds":"known"}`, `{"sessionIds":["unknown"]}`} {
		server.handleEvictSessions(json.RawMessage(raw))
	}
	if server.lookupSession("known") != current {
		t.Fatal("invalid or unknown eviction changed the resident session")
	}
}

func TestEvictSessionsUnloadsOnlyIdleSessions(t *testing.T) {
	closed := 0
	idle := &session{id: "idle", close: func() { closed++ }}
	busy := &session{id: "busy", running: true, close: func() { t.Fatal("busy session closed") }}
	server := &Server{sessions: map[string]*session{"idle": idle, "busy": busy}}

	server.handleEvictSessions(json.RawMessage(`{"sessionIds":["busy","idle","idle"]}`))

	if server.lookupSession("busy") != busy {
		t.Fatal("busy session was evicted")
	}
	if server.lookupSession("idle") != nil || closed != 1 {
		t.Fatalf("idle session resident=%v close calls=%d", server.lookupSession("idle") != nil, closed)
	}
}

func TestSessionHasLiveWorkCoversQueuedAndAuxiliaryActivity(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*session)
	}{
		{name: "run completion", mutate: func(s *session) { s.runDone = make(chan struct{}) }},
		{name: "btw", mutate: func(s *session) { s.btwDone = make(chan struct{}) }},
		{name: "recap", mutate: func(s *session) { s.recapDone = make(chan struct{}) }},
		{name: "suggest", mutate: func(s *session) { s.suggestDone = make(chan struct{}) }},
		{name: "wake", mutate: func(s *session) { s.wakeQueue = []syntheticWake{{}} }},
		{name: "interjection", mutate: func(s *session) { s.interjectionQueue = []agent.Interjection{{}} }},
		{name: "prompt", mutate: func(s *session) { s.promptQueue = []queuedPrompt{{}} }},
		{name: "starting prompt", mutate: func(s *session) { s.startingPromptID = "prompt" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			current := &session{id: test.name}
			test.mutate(current)
			server := &Server{sessions: map[string]*session{current.id: current}}
			params, err := json.Marshal(map[string]any{"sessionIds": []string{current.id}})
			if err != nil {
				t.Fatal(err)
			}
			server.handleEvictSessions(params)
			if server.lookupSession(current.id) != current {
				t.Fatal("active session was evicted")
			}
		})
	}
}

func TestTaskSnapshotHasLiveWork(t *testing.T) {
	completed := true
	tests := []struct {
		name     string
		snapshot agent.TaskSnapshot
		want     bool
	}{
		{name: "empty"},
		{name: "completed work", snapshot: agent.TaskSnapshot{Subagents: []tools.SubagentResult{{Status: "completed"}}, Processes: []tools.ProcessSnapshot{{Completed: completed}}}},
		{name: "running subagent", snapshot: agent.TaskSnapshot{Subagents: []tools.SubagentResult{{Status: "running"}}}, want: true},
		{name: "active process", snapshot: agent.TaskSnapshot{Processes: []tools.ProcessSnapshot{{}}}, want: true},
		{name: "scheduled task", snapshot: agent.TaskSnapshot{Scheduled: []tools.ScheduledTaskCreated{{TaskID: "scheduled"}}}, want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := taskSnapshotHasLiveWork(test.snapshot); got != test.want {
				t.Fatalf("live=%v want=%v", got, test.want)
			}
		})
	}
}

func TestTerminalManagerHasLiveSession(t *testing.T) {
	exitCode := 0
	manager := newTerminalManager(nil)
	manager.commands["other"] = &commandTerminal{sessionID: "other"}
	manager.commands["exited"] = &commandTerminal{sessionID: "session", exitCode: &exitCode}
	manager.commands["background"] = &commandTerminal{sessionID: "session", backgrounded: true}
	if manager.hasLiveSession("session") {
		t.Fatal("exited and detached commands reported live")
	}
	manager.commands["live"] = &commandTerminal{sessionID: "session"}
	if !manager.hasLiveSession("session") {
		t.Fatal("active foreground command reported idle")
	}
}

func TestEvictSessionsRouteIsFireAndForget(t *testing.T) {
	var output bytes.Buffer
	server := &Server{Factory: func(context.Context, SessionConfig, tools.Approver, io.Writer, io.Writer) (*agent.Runner, func(), error) {
		return nil, nil, nil
	}}
	input := bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"x.ai/internal/evict_sessions","params":{"sessionIds":["unknown"]}}`)
	if err := server.Serve(context.Background(), input, &output); err != nil {
		t.Fatal(err)
	}
	if output.Len() != 0 {
		t.Fatalf("notification produced output: %s", output.String())
	}
}
