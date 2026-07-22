package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

type synchronizedBuffer struct {
	mu sync.Mutex
	bytes.Buffer
}

func (b *synchronizedBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.Write(data)
}

func (b *synchronizedBuffer) snapshot() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.Buffer.Bytes()...)
}

func TestFuzzySearchExtensionLifecycleAndRouting(t *testing.T) {
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("ripgrep is not installed")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	output := &synchronizedBuffer{}
	server := &Server{output: output, sessions: map[string]*session{"fuzzy-session": {id: "fuzzy-session", cwd: root}}}
	server.handleFuzzySearch(context.Background(), message{
		ID: json.RawMessage("1"), Method: "x.ai/search/fuzzy/open",
		Params: json.RawMessage(`{"sessionId":"fuzzy-session","requestId":"wire-search","_meta":{"clientId":{"instanceId":"relay","connId":"client"}}}`),
	})
	server.handleFuzzySearch(context.Background(), message{
		ID: json.RawMessage("2"), Method: "x.ai/search/fuzzy/change",
		Params: json.RawMessage(`{"searchId":"wire-search","query":"main","limit":1}`),
	})
	var messages []map[string]any
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		messages = decodeACPBytes(t, output.snapshot())
		if len(messages) >= 3 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(messages) != 3 {
		t.Fatalf("messages=%#v", messages)
	}
	var opened, changed, status map[string]any
	for _, item := range messages {
		switch item["id"] {
		case float64(1):
			opened = item
		case float64(2):
			changed = item
		default:
			if item["method"] == "x.ai/search/fuzzy/status" {
				status = item
			}
		}
	}
	if opened["result"].(map[string]any)["result"].(map[string]any)["searchId"] != "wire-search" || changed["result"].(map[string]any)["error"] != nil {
		t.Fatalf("opened=%#v changed=%#v", opened, changed)
	}
	params := status["params"].(map[string]any)
	meta := params["_meta"].(map[string]any)["targetClientId"].(map[string]any)
	if params["sessionId"] != "fuzzy-session" || params["done"] != true || params["total"].(float64) != 1 || meta["instanceId"] != "relay" || meta["connId"] != "client" {
		t.Fatalf("unexpected status: %#v", params)
	}
	server.handleFuzzySearch(context.Background(), message{ID: json.RawMessage("3"), Method: "x.ai/search/fuzzy/close", Params: json.RawMessage(`{"searchId":"wire-search"}`)})
	server.handleFuzzySearch(context.Background(), message{ID: json.RawMessage("4"), Method: "x.ai/search/fuzzy/change", Params: json.RawMessage(`{"searchId":"wire-search","query":"main"}`)})
	messages = decodeACPBytes(t, output.snapshot())
	if messages[len(messages)-2]["result"].(map[string]any)["result"].(map[string]any)["closed"] != true || messages[len(messages)-1]["error"].(map[string]any)["code"].(float64) != -32602 {
		t.Fatalf("unexpected close results: %#v", messages[len(messages)-2:])
	}
	server.fuzzyManager().CloseAll()
}

func TestFuzzySearchExtensionValidatesRequests(t *testing.T) {
	var output bytes.Buffer
	server := &Server{output: &output, sessions: map[string]*session{}}
	requests := []message{
		{ID: json.RawMessage("1"), Method: "x.ai/search/fuzzy/open", Params: json.RawMessage(`{"cwd":"/tmp","_meta":{"clientId":{"instanceId":"only"}}}`)},
		{ID: json.RawMessage("2"), Method: "x.ai/search/fuzzy/change", Params: json.RawMessage(`{"searchId":"missing","query":"x","limit":-1}`)},
		{ID: json.RawMessage("3"), Method: "x.ai/search/fuzzy/close", Params: json.RawMessage(`{}`)},
		{ID: json.RawMessage("4"), Method: "x.ai/search/fuzzy/change", Params: json.RawMessage(`{"searchId":"missing"}`)},
	}
	for _, request := range requests {
		server.handleFuzzySearch(context.Background(), request)
	}
	for _, response := range decodeACPOutput(t, output.Bytes()) {
		if response["error"].(map[string]any)["code"].(float64) != -32602 {
			t.Fatalf("unexpected response: %#v", response)
		}
	}
}

func TestFuzzySearchExtensionAllowsExplicitEmptySearchID(t *testing.T) {
	root := t.TempDir()
	var output bytes.Buffer
	server := &Server{output: &output, sessions: map[string]*session{}}
	server.handleFuzzySearch(context.Background(), message{ID: json.RawMessage("1"), Method: "x.ai/search/fuzzy/open", Params: json.RawMessage(`{"cwd":` + strconv.Quote(root) + `,"requestId":""}`)})
	server.handleFuzzySearch(context.Background(), message{ID: json.RawMessage("2"), Method: "x.ai/search/fuzzy/close", Params: json.RawMessage(`{"searchId":""}`)})
	messages := decodeACPOutput(t, output.Bytes())
	if messages[0]["result"].(map[string]any)["result"].(map[string]any)["searchId"] != "" || messages[1]["result"].(map[string]any)["result"].(map[string]any)["closed"] != true {
		t.Fatalf("messages=%#v", messages)
	}
	server.fuzzyManager().CloseAll()
}

func decodeACPBytes(t *testing.T, data []byte) []map[string]any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(data))
	var messages []map[string]any
	for {
		var message map[string]any
		if err := decoder.Decode(&message); err == io.EOF {
			return messages
		} else if err != nil {
			t.Fatal(err)
		}
		messages = append(messages, message)
	}
}
