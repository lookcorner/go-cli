package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lookcorner/go-cli/internal/workspace"
)

func TestLifecycleToolQueryAndDiagnostics(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "main.go")
	if err := os.WriteFile(path, []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	client, err := Start(context.Background(), ProcessConfig{
		Name: "fixture", Command: os.Args[0],
		Args:       []string{"-test.run=TestLSPHelperProcess"},
		Env:        map[string]string{"GORK_GO_LSP_HELPER": "1"},
		Extensions: []string{"go"}, Root: root, Stderr: io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager(ws)
	if err := manager.Add(client); err != nil {
		t.Fatal(err)
	}
	hover, err := manager.Tool().Execute(context.Background(), json.RawMessage(
		`{"server":"fixture","operation":"hover","path":"main.go","line":1,"character":1}`,
	))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(hover, "fixture hover") {
		t.Fatalf("unexpected hover result: %s", hover)
	}
	deadline := time.Now().Add(time.Second)
	for {
		diagnostics, err := manager.Tool().Execute(context.Background(), json.RawMessage(
			`{"server":"fixture","operation":"diagnostics","path":"main.go"}`,
		))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(diagnostics, "fixture diagnostic") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("diagnostics were not published: %s", diagnostics)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestReadContentLength(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("Content-Type: application/vscode-jsonrpc; charset=utf-8\r\ncontent-length: 12\r\n\r\n"))
	length, err := readContentLength(reader)
	if err != nil {
		t.Fatal(err)
	}
	if length != 12 {
		t.Fatalf("got %d, want 12", length)
	}
}

func TestLSPHelperProcess(t *testing.T) {
	if os.Getenv("GORK_GO_LSP_HELPER") != "1" {
		return
	}
	reader := bufio.NewReader(os.Stdin)
	for {
		length, err := readContentLength(reader)
		if err != nil {
			os.Exit(0)
		}
		body := make([]byte, length)
		if _, err := io.ReadFull(reader, body); err != nil {
			os.Exit(2)
		}
		var message rpcMessage
		if json.Unmarshal(body, &message) != nil {
			os.Exit(3)
		}
		if message.Method == "exit" {
			os.Exit(0)
		}
		if message.Method == "textDocument/didOpen" {
			var params struct {
				TextDocument struct {
					URI string `json:"uri"`
				} `json:"textDocument"`
			}
			_ = json.Unmarshal(message.Params, &params)
			writeLSPFixtureMessage(map[string]any{
				"jsonrpc": "2.0", "method": "textDocument/publishDiagnostics",
				"params": map[string]any{"uri": params.TextDocument.URI, "diagnostics": []any{map[string]any{
					"message": "fixture diagnostic", "severity": 2,
					"range": map[string]any{
						"start": map[string]any{"line": 0, "character": 0},
						"end":   map[string]any{"line": 0, "character": 1},
					},
				}}},
			})
			continue
		}
		if len(message.ID) == 0 {
			continue
		}
		var id any
		_ = json.Unmarshal(message.ID, &id)
		var result any
		switch message.Method {
		case "initialize":
			result = map[string]any{"capabilities": map[string]any{"hoverProvider": true}}
		case "textDocument/hover":
			result = map[string]any{"contents": map[string]any{"kind": "plaintext", "value": "fixture hover"}}
		case "shutdown":
			result = nil
		default:
			writeLSPFixtureMessage(map[string]any{
				"jsonrpc": "2.0", "id": id,
				"error": map[string]any{"code": -32601, "message": fmt.Sprintf("unknown method %s", message.Method)},
			})
			continue
		}
		writeLSPFixtureMessage(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	}
}

func writeLSPFixtureMessage(value any) {
	body, err := json.Marshal(value)
	if err != nil {
		os.Exit(4)
	}
	if _, err := fmt.Fprintf(os.Stdout, "Content-Length: %d\r\n\r\n", len(body)); err != nil {
		os.Exit(5)
	}
	if _, err := os.Stdout.Write(body); err != nil {
		os.Exit(6)
	}
}
