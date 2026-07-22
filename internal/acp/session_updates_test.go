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
)

const sessionUpdatesTinyPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="

func sessionUpdatesFixture(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	logger, err := sessionlog.NewLoggerWithID(dir, "bulk-updates")
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []struct {
		kind string
		data any
	}{
		{"session_metadata", map[string]any{"cwd": "/work"}},
		{"user_prompt", map[string]any{"text": "first"}},
		{"model_response", map[string]any{"text": "answer", "response_id": "r1", "tool_call_count": 0}},
	} {
		if err := logger.Append(event.kind, event.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := logger.AppendPrompt("inspect", []sessionlog.Content{
		{Type: "image", URI: "data:image/png;base64," + sessionUpdatesTinyPNG},
		{Type: "image", URI: "https://example.com/image.png"},
	}); err != nil {
		t.Fatal(err)
	}
	for _, event := range []struct {
		kind string
		data any
	}{
		{"tool_call", map[string]any{"call_id": "call-1", "name": "read_file", "arguments": json.RawMessage(`{"path":"a.go"}`)}},
		{"tool_result", map[string]any{"call_id": "call-1", "name": "read_file", "output": "contents", "failed": false}},
		{"session_mode", map[string]any{"mode_id": "plan"}},
		{"xai_session_notification", map[string]any{
			"sessionId": "bulk-updates",
			"update":    map[string]any{"sessionUpdate": "auto_compact_completed", "tokens_after": 10},
			"_meta":     map[string]any{"eventId": "bulk-updates-9", "agentTimestampMs": 12},
		}},
	} {
		if err := logger.Append(event.kind, event.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	return dir, logger.Path()
}

func TestSessionUpdatesProjectsLiveEventsAndMultimodalContent(t *testing.T) {
	_, path := sessionUpdatesFixture(t)
	updates, err := sessionUpdateEnvelopes(path, "bulk-updates")
	if err != nil {
		t.Fatal(err)
	}
	if len(updates) != 9 {
		t.Fatalf("updates=%#v", updates)
	}
	if starts := sessionPromptStarts(updates); len(starts) != 2 || starts[0] != 0 || starts[1] != 2 {
		t.Fatalf("prompt starts=%v", starts)
	}
	image := updates[3].Params["update"].(map[string]any)["content"].(map[string]any)
	remote := updates[4].Params["update"].(map[string]any)["content"].(map[string]any)
	if image["type"] != "image" || image["data"] != sessionUpdatesTinyPNG || image["mimeType"] != "image/png" || remote["uri"] != "https://example.com/image.png" {
		t.Fatalf("image=%#v remote=%#v", image, remote)
	}
	tool := updates[5].Params["update"].(map[string]any)
	result := updates[6].Params["update"].(map[string]any)
	if tool["sessionUpdate"] != "tool_call" || tool["toolCallId"] != "call-1" || tool["status"] != "in_progress" || result["sessionUpdate"] != "tool_call_update" || result["status"] != "completed" {
		t.Fatalf("tool=%#v result=%#v", tool, result)
	}
	mode := updates[7].Params["update"].(map[string]any)
	if mode["sessionUpdate"] != "current_mode_update" || mode["currentModeId"] != "plan" {
		t.Fatalf("mode=%#v", mode)
	}
	if updates[8].Method != "_x.ai/session/update" || sessionUpdatesLastEventID(updates) != "bulk-updates-9" {
		t.Fatalf("last=%#v", updates[8])
	}
}

func TestSessionUpdatesProjectsFailedToolsAndLifecycleEvents(t *testing.T) {
	dir := t.TempDir()
	logger, err := sessionlog.NewLoggerWithID(dir, "lifecycle-updates")
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []struct {
		kind string
		data any
	}{
		{"tool_result", map[string]any{"call_id": "failed-1", "output": "denied", "failed": true}},
		{"subagent_spawned", map[string]any{"subagent_id": "child-1"}},
		{"subagent_finished", map[string]any{"sessionUpdate": "subagent_finished", "subagent_id": "child-1"}},
		{"task_backgrounded", map[string]any{"task_id": "task-1"}},
		{"task_completed", map[string]any{"task_id": "task-1"}},
	} {
		if err := logger.Append(event.kind, event.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	updates, err := sessionUpdateEnvelopes(logger.Path(), "lifecycle-updates")
	if err != nil {
		t.Fatal(err)
	}
	if len(updates) != 5 {
		t.Fatalf("updates=%#v", updates)
	}
	failed := updates[0].Params["update"].(map[string]any)
	if failed["sessionUpdate"] != "tool_call_update" || failed["status"] != "failed" || failed["rawOutput"] != "denied" {
		t.Fatalf("failed tool=%#v", failed)
	}
	for index, kind := range []string{"subagent_spawned", "subagent_finished", "task_backgrounded", "task_completed"} {
		update := updates[index+1].Params["update"].(map[string]any)
		if updates[index+1].Method != "_x.ai/session/update" || update["sessionUpdate"] != kind {
			t.Fatalf("lifecycle %d=%#v", index, updates[index+1])
		}
	}
}

func TestSessionUpdatesPaginationTurnTailAndEmpty(t *testing.T) {
	dir, _ := sessionUpdatesFixture(t)
	var output bytes.Buffer
	server := &Server{SessionDir: dir, output: &output}
	offset, limit := int64(-4), 2
	server.handleSessionUpdates(message{ID: json.RawMessage("1"), Params: mustJSON(t, sessionUpdatesRequest{SessionID: "bulk-updates", CWD: "/work", Offset: &offset, Limit: &limit})})
	result := decodeACPOutput(t, output.Bytes())[0]["result"].(map[string]any)
	updates := result["updates"].([]any)
	if result["totalCount"] != float64(9) || result["hasMore"] != true || len(updates) != 2 {
		t.Fatalf("result=%#v", result)
	}
	if updates[0].(map[string]any)["params"].(map[string]any)["update"].(map[string]any)["sessionUpdate"] != "tool_call" {
		t.Fatalf("updates=%#v", updates)
	}

	output.Reset()
	turns := 1
	server.handleSessionUpdates(message{ID: json.RawMessage("2"), Params: mustJSON(t, sessionUpdatesRequest{SessionID: "bulk-updates", CWD: "/work", TurnIndex: &turns})})
	result = decodeACPOutput(t, output.Bytes())[0]["result"].(map[string]any)
	if len(result["updates"].([]any)) != 7 || result["hasMore"] != true || result["lastEventId"] != "bulk-updates-9" {
		t.Fatalf("turn result=%#v", result)
	}

	output.Reset()
	server.handleSessionUpdates(message{ID: json.RawMessage("3"), Params: json.RawMessage(`{"sessionId":"missing","cwd":"/work"}`)})
	result = decodeACPOutput(t, output.Bytes())[0]["result"].(map[string]any)
	if len(result["updates"].([]any)) != 0 || result["totalCount"] != float64(0) || result["hasMore"] != false {
		t.Fatalf("empty=%#v", result)
	}

	output.Reset()
	server.handleSessionUpdates(message{ID: json.RawMessage("4"), Params: json.RawMessage(`{"sessionId":"missing","cwd":"/work","stream":true}`)})
	result = decodeACPOutput(t, output.Bytes())[0]["result"].(map[string]any)
	if result["totalCount"] != float64(0) || result["chunkCount"] != float64(0) {
		t.Fatalf("empty stream=%#v", result)
	}
}

func TestSessionUpdatesStreamsChunksAndServeRoute(t *testing.T) {
	dir, _ := sessionUpdatesFixture(t)
	request := `{"jsonrpc":"2.0","id":5,"method":"x.ai/session/updates","params":{"sessionId":"bulk-updates","cwd":"/work","stream":true,"chunkSize":3,"_meta":{"clientId":"leader-1"}}}` + "\n"
	var output bytes.Buffer
	server := &Server{SessionDir: dir, Factory: func(context.Context, SessionConfig, tools.Approver, io.Writer, io.Writer) (*agent.Runner, func(), error) {
		return nil, nil, nil
	}}
	if err := server.Serve(context.Background(), strings.NewReader(request), &output); err != nil {
		t.Fatal(err)
	}
	messages := decodeACPOutput(t, output.Bytes())
	if len(messages) != 4 {
		t.Fatalf("messages=%#v", messages)
	}
	for index, item := range messages[:3] {
		params := item["params"].(map[string]any)
		if item["method"] != "x.ai/session/updates/chunk" || params["index"] != float64(index) || params["done"] != (index == 2) || params["_meta"].(map[string]any)["clientId"] != "leader-1" {
			t.Fatalf("chunk %d=%#v", index, item)
		}
	}
	result := messages[3]["result"].(map[string]any)
	if messages[3]["id"] != float64(5) || result["totalCount"] != float64(9) || result["chunkCount"] != float64(3) || result["lastEventId"] != "bulk-updates-9" {
		t.Fatalf("result=%#v", messages[3])
	}
}

func TestSessionUpdatesFiltersRewoundBranchAndValidatesParams(t *testing.T) {
	dir := t.TempDir()
	logger, err := sessionlog.NewLoggerWithID(dir, "rewound-updates")
	if err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]string{{"first", "one"}, {"dead", "two"}} {
		if err := logger.AppendPrompt(pair[0], nil); err != nil {
			t.Fatal(err)
		}
		if err := logger.Append("model_response", map[string]any{"text": pair[1], "response_id": pair[1], "tool_call_count": 0}); err != nil {
			t.Fatal(err)
		}
	}
	path := logger.Path()
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := sessionlog.Rewind(path, 1); err != nil {
		t.Fatal(err)
	}
	logger, _, err = sessionlog.Resume(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := logger.AppendPrompt("replacement", nil); err != nil {
		t.Fatal(err)
	}
	if err := logger.Append("model_response", map[string]any{"text": "three", "response_id": "three", "tool_call_count": 0}); err != nil {
		t.Fatal(err)
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	updates, err := sessionUpdateEnvelopes(path, "rewound-updates")
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(updates)
	if len(updates) != 4 || strings.Contains(string(encoded), "dead") || !strings.Contains(string(encoded), "replacement") {
		t.Fatalf("updates=%s", encoded)
	}

	var output bytes.Buffer
	(&Server{output: &output}).handleSessionUpdates(message{ID: json.RawMessage("9"), Params: json.RawMessage(`{"sessionId":"bad/id","cwd":""}`)})
	if decodeACPOutput(t, output.Bytes())[0]["error"].(map[string]any)["code"] != float64(-32602) {
		t.Fatalf("output=%s", output.String())
	}
}

func TestSessionUpdatesRejectsEscapingImageAsset(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "unsafe-updates.jsonl")
	line := `{"time":"2026-01-01T00:00:00Z","kind":"user_prompt","data":{"text":"inspect","content":[{"type":"image","uri":"../secret.png","mimeType":"image/png"}]}}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	server := &Server{SessionDir: dir, output: &output}
	server.handleSessionUpdates(message{ID: json.RawMessage("1"), Params: json.RawMessage(`{"sessionId":"unsafe-updates","cwd":"/work"}`)})
	response := decodeACPOutput(t, output.Bytes())[0]
	if response["error"].(map[string]any)["code"] != float64(-32000) {
		t.Fatalf("response=%#v", response)
	}
}

func TestSessionUpdatePageBoundsAndOffsetPrecedence(t *testing.T) {
	starts := []int{0, 2, 4}
	turns, offset, limit := 1, int64(-2), 1
	start, end, more := sessionUpdatePage(sessionUpdatesRequest{TurnIndex: &turns, Offset: &offset, Limit: &limit}, 6, starts)
	if start != 4 || end != 5 || !more {
		t.Fatalf("offset page=(%d,%d,%v)", start, end, more)
	}
	offset = -100
	start, end, more = sessionUpdatePage(sessionUpdatesRequest{Offset: &offset}, 6, starts)
	if start != 0 || end != 6 || more {
		t.Fatalf("wide tail=(%d,%d,%v)", start, end, more)
	}
	offset = 100
	start, end, more = sessionUpdatePage(sessionUpdatesRequest{Offset: &offset}, 6, starts)
	if start != 6 || end != 6 || more {
		t.Fatalf("past end=(%d,%d,%v)", start, end, more)
	}
}

func mustJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
