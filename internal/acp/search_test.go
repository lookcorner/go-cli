package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestContentSearchExtensionStreamsAndReturnsResults(t *testing.T) {
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("ripgrep is not installed")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n// searchable marker\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	server := &Server{output: &output, sessions: map[string]*session{"search-session": {id: "search-session", cwd: root}}}
	server.handleContentSearch(context.Background(), message{
		ID: json.RawMessage("1"), Method: "x.ai/search/content",
		Params: json.RawMessage(`{"sessionId":"search-session","pattern":"searchable","includeGlobs":["*.go"]}`),
	})
	messages := decodeACPOutput(t, output.Bytes())
	if len(messages) != 2 || messages[0]["method"] != "x.ai/search/content/status" {
		t.Fatalf("unexpected messages: %#v", messages)
	}
	status := messages[0]["params"].(map[string]any)
	if status["sessionId"] != "search-session" || status["done"] != true || status["totalMatches"].(float64) != 1 {
		t.Fatalf("unexpected status: %#v", status)
	}
	extension := messages[1]["result"].(map[string]any)
	result := extension["result"].(map[string]any)
	if extension["error"] != nil || result["totalFiles"].(float64) != 1 || result["truncated"] != false {
		t.Fatalf("unexpected result: %#v", extension)
	}
}

func TestContentSearchExtensionRequiresRoot(t *testing.T) {
	var output bytes.Buffer
	server := &Server{output: &output, sessions: map[string]*session{}}
	server.handleContentSearch(context.Background(), message{ID: json.RawMessage("1"), Params: json.RawMessage(`{"pattern":"needle"}`)})
	response := decodeACPOutput(t, output.Bytes())[0]
	if response["error"].(map[string]any)["code"].(float64) != -32602 {
		t.Fatalf("unexpected response: %#v", response)
	}
	output.Reset()
	server.handleContentSearch(context.Background(), message{ID: json.RawMessage("2"), Params: json.RawMessage(`{"cwd":"/tmp","pattern":"needle","maxMatches":-1}`)})
	response = decodeACPOutput(t, output.Bytes())[0]
	if response["error"].(map[string]any)["code"].(float64) != -32602 {
		t.Fatalf("negative limit was accepted: %#v", response)
	}
}

func TestContentSearchExtensionRequiresPatternField(t *testing.T) {
	var output bytes.Buffer
	server := &Server{output: &output, sessions: map[string]*session{}}
	server.handleContentSearch(context.Background(), message{ID: json.RawMessage("1"), Params: json.RawMessage(`{"cwd":"/tmp"}`)})
	response := decodeACPOutput(t, output.Bytes())[0]
	if response["error"].(map[string]any)["code"].(float64) != -32602 {
		t.Fatalf("missing pattern was accepted: %#v", response)
	}
}

func TestContentSearchExtensionEncodesEmptyFilesAsArray(t *testing.T) {
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("ripgrep is not installed")
	}
	var output bytes.Buffer
	server := &Server{output: &output, sessions: map[string]*session{}}
	params, _ := json.Marshal(map[string]any{"cwd": t.TempDir(), "pattern": "absent"})
	server.handleContentSearch(context.Background(), message{ID: json.RawMessage("1"), Params: params})
	messages := decodeACPOutput(t, output.Bytes())
	statusFiles, statusOK := messages[0]["params"].(map[string]any)["files"].([]any)
	resultFiles, resultOK := messages[1]["result"].(map[string]any)["result"].(map[string]any)["files"].([]any)
	if !statusOK || !resultOK || len(statusFiles) != 0 || len(resultFiles) != 0 {
		t.Fatalf("empty files were not arrays: %#v", messages)
	}
}
