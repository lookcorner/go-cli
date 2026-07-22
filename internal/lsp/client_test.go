package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
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
		InitializationOptions: map[string]any{"usePlaceholders": true},
		Settings: map[string]any{
			"gopls": map[string]any{"staticcheck": true, "nested": map[string]any{"value": "configured"}},
		},
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
	resolvedPath, _ := ws.Resolve(path)
	definitions, err := manager.Tool().CodeLocations(context.Background(), "definition", "main.go", 1, 9)
	if err != nil || len(definitions) != 1 || definitions[0].Path != resolvedPath || definitions[0].Range.Start.Character != 8 {
		t.Fatalf("definitions=%#v err=%v", definitions, err)
	}
	references, err := manager.Tool().CodeLocations(context.Background(), "references", "main.go", 1, 9)
	if err != nil || len(references) != 2 || references[1].Range.Start.Line != 2 {
		t.Fatalf("references=%#v err=%v", references, err)
	}
	symbols, err := manager.Tool().CodeSymbols(context.Background(), "fixture")
	if err != nil || len(symbols) != 1 || symbols[0].Name != "fixture" || symbols[0].Location.Path != resolvedPath {
		t.Fatalf("symbols=%#v err=%v", symbols, err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "package fixture\n" {
		t.Fatalf("workspace/applyEdit was not applied: %q err=%v", data, err)
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

func TestSocketLifecycle(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	serverDone := make(chan error, 1)
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			serverDone <- err
			return
		}
		defer connection.Close()
		reader := bufio.NewReader(connection)
		for {
			length, err := readContentLength(reader)
			if err != nil {
				serverDone <- err
				return
			}
			body := make([]byte, length)
			if _, err := io.ReadFull(reader, body); err != nil {
				serverDone <- err
				return
			}
			var message rpcMessage
			if err := json.Unmarshal(body, &message); err != nil {
				serverDone <- err
				return
			}
			if message.Method == "exit" {
				serverDone <- nil
				return
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
				result = map[string]any{"contents": "socket hover"}
			case "shutdown":
				result = nil
			default:
				serverDone <- fmt.Errorf("unexpected socket LSP method %q", message.Method)
				return
			}
			body, err = json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
			if err != nil {
				serverDone <- err
				return
			}
			if _, err := fmt.Fprintf(connection, "Content-Length: %d\r\n\r\n", len(body)); err == nil {
				_, err = connection.Write(body)
			}
			if err != nil {
				serverDone <- err
				return
			}
		}
	}()
	client, err := Start(context.Background(), ProcessConfig{
		Name: "socket-fixture", Command: listener.Addr().String(), Transport: "socket", Root: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Request(context.Background(), "textDocument/hover", map[string]any{})
	if err != nil || !strings.Contains(string(result), "socket hover") {
		t.Fatalf("socket hover=%s err=%v", result, err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("socket LSP server did not receive exit")
	}
}

func TestManagerRestartsCrashedServer(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "crashed")
	client, err := Start(context.Background(), ProcessConfig{
		Name: "restart-fixture", Command: os.Args[0],
		Args: []string{"-test.run=TestLSPHelperProcess"},
		Env: map[string]string{
			"GORK_GO_LSP_HELPER": "1", "GORK_GO_LSP_CRASH_MARKER": marker,
		},
		Extensions: []string{"go"}, Root: root, Stderr: io.Discard,
		InitializationOptions: map[string]any{"usePlaceholders": true},
		RestartOnCrash:        true, MaxRestarts: 2, RestartBackoff: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	ws, _ := workspace.Open(root)
	manager := NewManager(ws)
	if err := manager.Add(client); err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	tool := manager.Tool()
	request := json.RawMessage(`{"server":"restart-fixture","operation":"hover","path":"main.go","line":1,"character":1}`)
	if _, err := tool.Execute(context.Background(), request); err == nil {
		t.Fatal("first hover did not observe fixture crash")
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		result, err := tool.Execute(context.Background(), request)
		if err == nil && strings.Contains(result, "fixture hover") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("restarted LSP did not recover: result=%q err=%v", result, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestManagerReplaceSwapsReadyClients(t *testing.T) {
	root := t.TempDir()
	start := func(name string) *Client {
		client, err := Start(context.Background(), ProcessConfig{
			Name: name, Command: os.Args[0], Args: []string{"-test.run=TestLSPHelperProcess"},
			Env: map[string]string{"GORK_GO_LSP_HELPER": "1"}, Root: root, Stderr: io.Discard,
			InitializationOptions: map[string]any{"usePlaceholders": true},
		})
		if err != nil {
			t.Fatal(err)
		}
		return client
	}
	first := start("first")
	ws, _ := workspace.Open(root)
	manager := NewManager(ws)
	if err := manager.Replace([]*Client{first}); err != nil {
		t.Fatal(err)
	}
	second := start("second")
	if err := manager.Replace([]*Client{second}); err != nil {
		t.Fatal(err)
	}
	if names := strings.Join(manager.Names(), "|"); names != "second" {
		t.Fatalf("unexpected manager names: %s", names)
	}
	if err := manager.Replace([]*Client{second, second}); err == nil {
		t.Fatal("duplicate replacement unexpectedly succeeded")
	}
	if names := strings.Join(manager.Names(), "|"); names != "second" {
		t.Fatalf("failed replacement changed manager names: %s", names)
	}
	select {
	case <-first.doneSignal():
	case <-time.After(time.Second):
		t.Fatal("replaced client was not closed")
	}
	if err := manager.Replace(nil); err != nil {
		t.Fatal(err)
	}
	if len(manager.Names()) != 0 {
		t.Fatalf("manager was not emptied: %#v", manager.Names())
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestLSPHelperProcess(t *testing.T) {
	if os.Getenv("GORK_GO_LSP_HELPER") != "1" {
		return
	}
	reader := bufio.NewReader(os.Stdin)
	var documentURI string
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
		if message.Method == "workspace/didChangeConfiguration" {
			var params struct {
				Settings map[string]any `json:"settings"`
			}
			if json.Unmarshal(message.Params, &params) != nil || params.Settings["gopls"] == nil {
				os.Exit(7)
			}
			writeLSPFixtureMessage(map[string]any{
				"jsonrpc": "2.0", "id": 90, "method": "workspace/configuration",
				"params": map[string]any{"items": []any{
					map[string]any{"section": "gopls"},
					map[string]any{"section": "gopls.nested.value"},
					map[string]any{"section": "missing"},
				}},
			})
			continue
		}
		if message.Method == "textDocument/didOpen" {
			var params struct {
				TextDocument struct {
					URI string `json:"uri"`
				} `json:"textDocument"`
			}
			_ = json.Unmarshal(message.Params, &params)
			documentURI = params.TextDocument.URI
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
			writeLSPFixtureMessage(map[string]any{
				"jsonrpc": "2.0", "id": 91, "method": "workspace/applyEdit",
				"params": map[string]any{"label": "rename package", "edit": map[string]any{
					"documentChanges": []any{map[string]any{
						"textDocument": map[string]any{"uri": params.TextDocument.URI, "version": 1},
						"edits": []any{map[string]any{
							"range": map[string]any{
								"start": map[string]any{"line": 0, "character": 8},
								"end":   map[string]any{"line": 0, "character": 12},
							},
							"newText": "fixture",
						}},
					}},
				}},
			})
			continue
		}
		if len(message.ID) == 0 {
			continue
		}
		if message.Method == "" {
			switch string(message.ID) {
			case "90":
				var values []any
				if json.Unmarshal(message.Result, &values) != nil || len(values) != 3 || values[1] != "configured" || values[2] != nil || values[0].(map[string]any)["staticcheck"] != true {
					os.Exit(9)
				}
			case "91":
				var applied struct {
					Applied bool `json:"applied"`
				}
				if json.Unmarshal(message.Result, &applied) != nil || !applied.Applied {
					os.Exit(11)
				}
			default:
				os.Exit(8)
			}
			continue
		}
		var id any
		_ = json.Unmarshal(message.ID, &id)
		var result any
		switch message.Method {
		case "initialize":
			var params struct {
				InitializationOptions map[string]any `json:"initializationOptions"`
				Capabilities          map[string]any `json:"capabilities"`
			}
			if json.Unmarshal(message.Params, &params) != nil || params.InitializationOptions["usePlaceholders"] != true {
				os.Exit(10)
			}
			workspaceCaps, _ := params.Capabilities["workspace"].(map[string]any)
			workspaceEdit, _ := workspaceCaps["workspaceEdit"].(map[string]any)
			resourceOperations, _ := workspaceEdit["resourceOperations"].([]any)
			if len(resourceOperations) != 3 {
				os.Exit(12)
			}
			result = map[string]any{"capabilities": map[string]any{"hoverProvider": true}}
		case "textDocument/hover":
			if marker := os.Getenv("GORK_GO_LSP_CRASH_MARKER"); marker != "" {
				if _, err := os.Stat(marker); os.IsNotExist(err) {
					_ = os.WriteFile(marker, []byte("crashed"), 0o600)
					os.Exit(13)
				}
			}
			result = map[string]any{"contents": map[string]any{"kind": "plaintext", "value": "fixture hover"}}
		case "textDocument/definition":
			var params struct {
				TextDocument struct {
					URI string `json:"uri"`
				} `json:"textDocument"`
			}
			_ = json.Unmarshal(message.Params, &params)
			result = map[string]any{
				"targetUri": params.TextDocument.URI,
				"targetRange": map[string]any{
					"start": map[string]any{"line": 0, "character": 0}, "end": map[string]any{"line": 0, "character": 15},
				},
				"targetSelectionRange": map[string]any{
					"start": map[string]any{"line": 0, "character": 8}, "end": map[string]any{"line": 0, "character": 15},
				},
			}
		case "textDocument/references":
			var params struct {
				TextDocument struct {
					URI string `json:"uri"`
				} `json:"textDocument"`
			}
			_ = json.Unmarshal(message.Params, &params)
			result = []any{
				map[string]any{"uri": params.TextDocument.URI, "range": map[string]any{"start": map[string]any{"line": 0, "character": 8}, "end": map[string]any{"line": 0, "character": 15}}},
				map[string]any{"uri": params.TextDocument.URI, "range": map[string]any{"start": map[string]any{"line": 2, "character": 1}, "end": map[string]any{"line": 2, "character": 8}}},
			}
		case "workspace/symbol":
			result = []any{map[string]any{
				"name": "fixture", "kind": 13,
				"location": map[string]any{"uri": documentURI, "range": map[string]any{"start": map[string]any{"line": 0, "character": 8}, "end": map[string]any{"line": 0, "character": 15}}},
			}}
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
