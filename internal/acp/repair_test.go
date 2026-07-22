package acp

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/lookcorner/go-cli/internal/agent"
	sessionlog "github.com/lookcorner/go-cli/internal/session"
)

type repairHistoryStreamer struct {
	fixtureStreamer
	rewound []sessionlog.Message
}

func (s *repairHistoryStreamer) RewindHistory(messages []sessionlog.Message) {
	s.rewound = append([]sessionlog.Message(nil), messages...)
}

func TestSessionRepairRepairsResidentHistoryAndResetsResponseChain(t *testing.T) {
	logger, err := sessionlog.NewLoggerWithID(t.TempDir(), "resident")
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	for _, event := range []struct {
		kind string
		data any
	}{
		{"session_metadata", map[string]any{"cwd": "/work"}},
		{"user_prompt", map[string]any{"text": "before"}},
		{"model_response", map[string]any{"response_id": "response-1", "text": "answer", "tool_call_count": 0}},
		{"tool_result", map[string]any{"call_id": "orphan", "output": "bad"}},
	} {
		if err := logger.Append(event.kind, event.data); err != nil {
			t.Fatal(err)
		}
	}
	streamer := &repairHistoryStreamer{}
	runner := &agent.Runner{Client: streamer, Logger: logger, Model: "test"}
	current := &session{id: "resident", runner: runner, logPath: logger.Path(), previous: "response-1"}
	var output bytes.Buffer
	server := &Server{output: &output, sessions: map[string]*session{current.id: current}}
	server.handleSessionRepair(message{ID: json.RawMessage("1"), Params: json.RawMessage(`{"sessionId":"resident"}`)})
	messages := decodeACPOutput(t, output.Bytes())
	if len(messages) != 1 || messages[0]["error"] != nil {
		t.Fatalf("messages=%#v", messages)
	}
	result := messages[0]["result"].(map[string]any)
	if result["repaired"] != true || result["resident"] != true || result["dryRun"] != false || result["strippedToolResultIds"].([]any)[0] != "orphan" {
		t.Fatalf("result=%#v", result)
	}
	if current.previous != "" || len(streamer.rewound) != 2 || streamer.rewound[0].Text != "before" || streamer.rewound[1].Text != "answer" {
		t.Fatalf("previous=%q rewound=%#v", current.previous, streamer.rewound)
	}
}

func TestSessionRepairDryRunsNonResidentHistory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "offline.jsonl")
	content := []byte("{\"kind\":\"tool_result\",\"data\":{\"call_id\":\"orphan\"}}\n")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	server := &Server{SessionDir: dir, output: &output, sessions: make(map[string]*session)}
	server.handleSessionRepair(message{ID: json.RawMessage("2"), Params: json.RawMessage(`{"sessionId":"offline","dryRun":true}`)})
	result := decodeACPOutput(t, output.Bytes())[0]["result"].(map[string]any)
	if result["repaired"] != true || result["resident"] != false || result["dryRun"] != true {
		t.Fatalf("result=%#v", result)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(content, after) {
		t.Fatalf("dry run changed history: %q", after)
	}
}

func TestSessionRepairDryRunDoesNotResetResidentHistory(t *testing.T) {
	logger, err := sessionlog.NewLoggerWithID(t.TempDir(), "resident-dry-run")
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	if err := logger.Append("tool_result", map[string]any{"call_id": "orphan"}); err != nil {
		t.Fatal(err)
	}
	streamer := &repairHistoryStreamer{}
	current := &session{
		id: "resident-dry-run", runner: &agent.Runner{Client: streamer, Logger: logger},
		logPath: logger.Path(), previous: "response-1",
	}
	var output bytes.Buffer
	server := &Server{output: &output, sessions: map[string]*session{current.id: current}}
	server.handleSessionRepair(message{ID: json.RawMessage("1"), Params: json.RawMessage(`{"sessionId":"resident-dry-run","dryRun":true}`)})
	result := decodeACPOutput(t, output.Bytes())[0]["result"].(map[string]any)
	if result["repaired"] != true || current.previous != "response-1" || len(streamer.rewound) != 0 {
		t.Fatalf("result=%#v previous=%q rewound=%#v", result, current.previous, streamer.rewound)
	}
	events, err := sessionlog.Events(logger.Path(), "tool_result")
	if err != nil || len(events) != 1 {
		t.Fatalf("events=%#v err=%v", events, err)
	}
}

func TestSessionRepairRejectsBusyAndUnknownSessions(t *testing.T) {
	logger, err := sessionlog.NewLoggerWithID(t.TempDir(), "busy")
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	current := &session{id: "busy", runner: &agent.Runner{Logger: logger}, logPath: logger.Path(), running: true}
	var output bytes.Buffer
	server := &Server{SessionDir: t.TempDir(), output: &output, sessions: map[string]*session{"busy": current}}
	server.handleSessionRepair(message{ID: json.RawMessage("3"), Params: json.RawMessage(`{"sessionId":"busy"}`)})
	server.handleSessionRepair(message{ID: json.RawMessage("4"), Params: json.RawMessage(`{"sessionId":"missing"}`)})
	messages := decodeACPOutput(t, output.Bytes())
	if len(messages) != 2 || messages[0]["error"].(map[string]any)["code"] != float64(-32000) || messages[1]["error"].(map[string]any)["code"] != float64(-32004) {
		t.Fatalf("messages=%#v", messages)
	}
}
