package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/api"
	sessionlog "github.com/lookcorner/go-cli/internal/session"
	"github.com/lookcorner/go-cli/internal/tools"
)

func acpSuggestionSession(t *testing.T, streamer agent.ResponseStreamer) (*session, *sessionlog.Logger) {
	t.Helper()
	logger, err := sessionlog.NewLoggerWithID(t.TempDir(), "suggest-session")
	if err != nil {
		t.Fatal(err)
	}
	if err := logger.AppendPrompt("fix the parser", nil); err != nil {
		t.Fatal(err)
	}
	if err := logger.Append("model_response", map[string]any{"response_id": "r1", "text": "The parser is fixed.", "tool_call_count": 0}); err != nil {
		t.Fatal(err)
	}
	return &session{id: "suggest-session", cwd: "/work", runner: &agent.Runner{Client: streamer, SessionPath: logger.Path()}}, logger
}

func TestSuggestPromptReturnsGenerationAndSuggestion(t *testing.T) {
	streamer := &acpRecapStreamer{result: api.StreamResult{Text: "commit this"}}
	current, logger := acpSuggestionSession(t, streamer)
	defer logger.Close()
	var output bytes.Buffer
	server := &Server{output: &output, sessions: map[string]*session{current.id: current}}
	server.handleSuggestPrompt(context.Background(), message{ID: json.RawMessage("7"), Params: json.RawMessage(`{"generation":42,"sessionId":"suggest-session","model":"fast-model"}`)})
	server.wg.Wait()
	messages := decodeACPOutput(t, output.Bytes())
	result := messages[0]["result"].(map[string]any)
	if messages[0]["id"] != float64(7) || result["suggestion"] != "commit this" || result["generation"] != float64(42) {
		t.Fatalf("messages=%#v", messages)
	}
	streamer.mu.Lock()
	defer streamer.mu.Unlock()
	if len(streamer.requests) != 1 || streamer.requests[0].Model != "fast-model" || streamer.requests[0].PreviousResponseID != "" || len(streamer.requests[0].Tools) != 0 {
		t.Fatalf("requests=%#v", streamer.requests)
	}
}

func TestSuggestPromptDegradesToNull(t *testing.T) {
	for _, test := range []struct {
		name     string
		sessions map[string]*session
		params   string
	}{
		{name: "unknown session", sessions: map[string]*session{}, params: `{"generation":1,"sessionId":"missing"}`},
		{name: "no session", sessions: map[string]*session{}, params: `{"generation":2}`},
		{name: "closed session", sessions: map[string]*session{"closed": {id: "closed", closed: true}}, params: `{"generation":3,"sessionId":"closed"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			server := &Server{output: &output, sessions: test.sessions}
			server.handleSuggestPrompt(context.Background(), message{ID: json.RawMessage("1"), Params: json.RawMessage(test.params)})
			result := decodeACPOutput(t, output.Bytes())[0]["result"].(map[string]any)
			if result["suggestion"] != nil {
				t.Fatalf("result=%#v", result)
			}
		})
	}
	var invalid bytes.Buffer
	server := &Server{output: &invalid, sessions: map[string]*session{}}
	server.handleSuggestPrompt(context.Background(), message{ID: json.RawMessage("2"), Params: json.RawMessage(`{}`)})
	if decodeACPOutput(t, invalid.Bytes())[0]["error"].(map[string]any)["code"] != float64(-32602) {
		t.Fatalf("response=%s", invalid.String())
	}
}

func TestSuggestPromptFailureAndConcurrentRequestReturnNull(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	streamer := &acpRecapStreamer{started: started, release: release, err: errors.New("offline")}
	current, logger := acpSuggestionSession(t, streamer)
	defer logger.Close()
	var output bytes.Buffer
	server := &Server{output: &output, sessions: map[string]*session{current.id: current}}
	server.handleSuggestPrompt(context.Background(), message{ID: json.RawMessage("1"), Params: json.RawMessage(`{"generation":1,"sessionId":"suggest-session"}`)})
	<-started
	server.handleSuggestPrompt(context.Background(), message{ID: json.RawMessage("2"), Params: json.RawMessage(`{"generation":2,"sessionId":"suggest-session"}`)})
	close(release)
	server.wg.Wait()
	messages := decodeACPOutput(t, output.Bytes())
	if len(messages) != 2 {
		t.Fatalf("messages=%#v", messages)
	}
	for _, item := range messages {
		if item["result"].(map[string]any)["suggestion"] != nil {
			t.Fatalf("messages=%#v", messages)
		}
	}
}

func TestSuggestPromptCloseCancelsAndWaits(t *testing.T) {
	started := make(chan struct{})
	streamer := &acpRecapStreamer{started: started, release: make(chan struct{})}
	current, logger := acpSuggestionSession(t, streamer)
	closed := false
	current.close = func() { closed = true; _ = logger.Close() }
	var output bytes.Buffer
	server := &Server{output: &output, sessions: map[string]*session{current.id: current}}
	server.handleSuggestPrompt(context.Background(), message{ID: json.RawMessage("1"), Params: json.RawMessage(`{"generation":1,"sessionId":"suggest-session"}`)})
	<-started
	if !server.closeSession(current.id) || !closed {
		t.Fatal("session did not wait for suggestion cancellation")
	}
	server.wg.Wait()
	result := decodeACPOutput(t, output.Bytes())[0]["result"].(map[string]any)
	if result["suggestion"] != nil {
		t.Fatalf("result=%#v", result)
	}
}

func TestSuggestPromptServeRoute(t *testing.T) {
	input := bytes.NewBufferString("{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"x.ai/suggestPrompt\",\"params\":{\"generation\":9,\"sessionId\":\"missing\"}}\n")
	var output bytes.Buffer
	server := &Server{SessionDir: t.TempDir(), Factory: func(context.Context, SessionConfig, tools.Approver, io.Writer, io.Writer) (*agent.Runner, func(), error) {
		return nil, nil, errors.New("factory must not be called")
	}}
	if err := server.Serve(context.Background(), input, &output); err != nil {
		t.Fatal(err)
	}
	result := decodeACPOutput(t, output.Bytes())[0]["result"].(map[string]any)
	if result["generation"] != float64(9) || result["suggestion"] != nil {
		t.Fatalf("result=%#v", result)
	}
}

func TestShellSuggestServeRouteAndWireContract(t *testing.T) {
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "My File.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	request := fmt.Sprintf("{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"x.ai/suggest\",\"params\":{\"text\":\"cat My\",\"cursor\":6,\"cwd\":%q,\"limit\":10,\"generation\":12,\"tokenOnly\":true}}\n", cwd)
	var output bytes.Buffer
	server := &Server{SessionDir: t.TempDir(), Factory: func(context.Context, SessionConfig, tools.Approver, io.Writer, io.Writer) (*agent.Runner, func(), error) {
		return nil, nil, errors.New("factory must not be called")
	}}
	if err := server.Serve(context.Background(), bytes.NewBufferString(request), &output); err != nil {
		t.Fatal(err)
	}
	result := decodeACPOutput(t, output.Bytes())[0]["result"].(map[string]any)
	rows := result["completions"].([]any)
	if result["generation"] != float64(12) || result["ghost"] != nil || len(rows) != 1 {
		t.Fatalf("result=%#v", result)
	}
	row := rows[0].(map[string]any)
	rangeValue := row["replaceRange"].([]any)
	if row["display"] != "My File.txt" || row["insertText"] != `cat My\ File.txt` || row["tokenText"] != `My\ File.txt` || row["source"] != "file" || rangeValue[0] != float64(4) || rangeValue[1] != float64(6) {
		t.Fatalf("row=%#v", row)
	}
}

func TestShellSuggestAIAndValidation(t *testing.T) {
	streamer := &acpRecapStreamer{result: api.StreamResult{Text: "git commit --amend"}}
	current, logger := acpSuggestionSession(t, streamer)
	defer logger.Close()
	var output bytes.Buffer
	server := &Server{SessionDir: t.TempDir(), output: &output, sessions: map[string]*session{current.id: current}}
	server.handleSuggest(context.Background(), message{ID: json.RawMessage("1"), Params: json.RawMessage(`{"text":"git","cursor":3,"cwd":"/work","limit":10,"generation":3,"includeAi":true,"aiModel":"fast-model","sessionId":"suggest-session"}`)})
	server.wg.Wait()
	result := decodeACPOutput(t, output.Bytes())[0]["result"].(map[string]any)
	ghost := result["ghost"].(map[string]any)
	if ghost["fullText"] != "git commit --amend" || ghost["source"] != "ai" || result["generation"] != float64(3) {
		t.Fatalf("result=%#v", result)
	}
	streamer.mu.Lock()
	if len(streamer.requests) != 1 || streamer.requests[0].Model != "fast-model" || streamer.requests[0].MaxOutputTokens != 50 {
		t.Fatalf("requests=%#v", streamer.requests)
	}
	streamer.mu.Unlock()

	var invalid bytes.Buffer
	(&Server{output: &invalid}).handleSuggest(context.Background(), message{ID: json.RawMessage("2"), Params: json.RawMessage(`{"text":"git"}`)})
	if decodeACPOutput(t, invalid.Bytes())[0]["error"].(map[string]any)["code"] != float64(-32602) {
		t.Fatalf("invalid=%s", invalid.String())
	}
}
