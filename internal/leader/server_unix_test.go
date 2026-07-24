//go:build unix

package leader

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestServerDiscoveryControlAndCleanup(t *testing.T) {
	root := shortLeaderTempDir(t)
	ctx, cancel := context.WithCancel(context.Background())
	server, err := Start(ctx, ServerConfig{Root: root, BinaryVersion: "1.2.3", NoExitOnDisconnect: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		_ = server.Close()
	})
	server.MarkReady()
	if _, err := Start(context.Background(), ServerConfig{Root: root}); err == nil {
		t.Fatal("second leader unexpectedly acquired the same lock")
	}

	descriptors, err := Discover(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(descriptors) != 1 || descriptors[0].State != StateReachable ||
		descriptors[0].LiveInfo == nil || descriptors[0].LiveInfo.LeaderBinaryVersion != "1.2.3" {
		t.Fatalf("descriptors=%+v", descriptors)
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"leader.sock", "leader.lock"} {
		if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Fatalf("%s was not cleaned up: %v", name, err)
		}
	}
}

func TestServerRoutesACPRequestsAndSessionsByClient(t *testing.T) {
	root := shortLeaderTempDir(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server, err := Start(ctx, ServerConfig{Root: root, NoExitOnDisconnect: true})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	server.MarkReady()

	first := connectLeaderClient(t, filepath.Join(root, "leader.sock"))
	defer first.Close()
	second := connectLeaderClient(t, filepath.Join(root, "leader.sock"))
	defer second.Close()

	writeClientACP(t, first, `{"jsonrpc":"2.0","id":"first","method":"session/new","params":{}}`)
	firstRequest := readACPLine(t, server)
	writeClientACP(t, second, `{"jsonrpc":"2.0","id":"second","method":"session/new","params":{}}`)
	secondRequest := readACPLine(t, server)
	if string(firstRequest.ID) == string(secondRequest.ID) || firstRequest.Method != "session/new" || secondRequest.Method != "session/new" {
		t.Fatalf("first=%+v second=%+v", firstRequest, secondRequest)
	}
	var firstParams struct {
		Meta map[string]any `json:"_meta"`
	}
	if json.Unmarshal(firstRequest.Params, &firstParams) != nil ||
		firstParams.Meta["clientIdentifier"] != "test" ||
		firstParams.Meta["codeNavEnabled"] != false ||
		firstParams.Meta["clientTerminal"] != false {
		t.Fatalf("first params=%s", firstRequest.Params)
	}

	writeACPLine(t, server, rpcEnvelope{JSONRPC: "2.0", ID: firstRequest.ID, Result: json.RawMessage(`{"sessionId":"session-a"}`)})
	writeACPLine(t, server, rpcEnvelope{JSONRPC: "2.0", ID: secondRequest.ID, Result: json.RawMessage(`{"sessionId":"session-b"}`)})
	assertClientResponse(t, first, `"first"`, "session-a")
	assertClientResponse(t, second, `"second"`, "session-b")

	writeClientACP(t, first, `{"jsonrpc":"2.0","id":2,"method":"session/prompt","params":{"sessionId":"session-b","prompt":[]}}`)
	var rejected struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	}
	readClientMessage(t, first, &rejected)
	if rejected.Type != "error" || rejected.Message != "ACP session belongs to another leader client" {
		t.Fatalf("rejected=%+v", rejected)
	}

	writeACPLine(t, server, rpcEnvelope{
		JSONRPC: "2.0", Method: "session/update",
		Params: json.RawMessage(`{"sessionId":"session-b","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"second"}}}`),
	})
	var routed struct {
		Type    string `json:"type"`
		Payload string `json:"payload"`
	}
	readClientMessage(t, second, &routed)
	if routed.Type != "acp" || !json.Valid([]byte(routed.Payload)) || !containsJSONText(routed.Payload, "second") {
		t.Fatalf("routed=%+v", routed)
	}
	_ = first.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	if err := readMessage(first, &routed); err == nil {
		t.Fatal("session-b notification leaked to the first client")
	}
}

func TestServerReadinessAndDefaultDisconnectShutdown(t *testing.T) {
	root := shortLeaderTempDir(t)
	server, err := Start(context.Background(), ServerConfig{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := net.Dial("unix", filepath.Join(root, "leader.sock"))
	if err != nil {
		t.Fatal(err)
	}
	if err := writeMessage(connection, map[string]any{
		"type": "register", "client_type": "test", "mode": "stdio", "capabilities": map[string]any{},
	}); err != nil {
		t.Fatal(err)
	}
	var registered struct {
		Type  string `json:"type"`
		Ready bool   `json:"ready"`
	}
	readClientMessage(t, connection, &registered)
	if registered.Type != "registered" || registered.Ready {
		t.Fatalf("registered=%+v", registered)
	}
	server.MarkReady()
	var ready struct {
		Type string `json:"type"`
	}
	readClientMessage(t, connection, &ready)
	if ready.Type != "leader_ready" {
		t.Fatalf("ready=%+v", ready)
	}
	if err := writeMessage(connection, map[string]any{"type": "disconnect"}); err != nil {
		t.Fatal(err)
	}
	_ = connection.Close()
	done := make(chan error, 1)
	go func() { done <- server.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("leader did not exit after its last client disconnected")
	}
}

func TestServerInjectsClientCapabilities(t *testing.T) {
	root := shortLeaderTempDir(t)
	server, err := Start(context.Background(), ServerConfig{Root: root, NoExitOnDisconnect: true})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	server.MarkReady()
	connection, err := net.Dial("unix", filepath.Join(root, "leader.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if err := writeMessage(connection, map[string]any{
		"type": "register", "client_type": "editor", "mode": "stdio",
		"capabilities": map[string]any{
			"yolo_mode": true, "auto_mode": true, "default_model": "grok-fast",
			"code_nav_enabled": true, "terminal": true, "fs_read": true, "fs_write": false,
		},
	}); err != nil {
		t.Fatal(err)
	}
	var registered map[string]any
	readClientMessage(t, connection, &registered)
	writeClientACP(t, connection, `{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"_meta":{}}}`)
	initialize := readACPLine(t, server)
	var initializeParams struct {
		Meta map[string]any `json:"_meta"`
	}
	if json.Unmarshal(initialize.Params, &initializeParams) != nil || initializeParams.Meta["clientIdentifier"] != "editor" {
		t.Fatalf("initialize params=%s", initialize.Params)
	}
	writeClientACP(t, connection, `{"jsonrpc":"2.0","id":1,"method":"session/new","params":{"_meta":{"modelId":"explicit"}}}`)
	request := readACPLine(t, server)
	var params struct {
		Meta map[string]any `json:"_meta"`
	}
	if err := json.Unmarshal(request.Params, &params); err != nil {
		t.Fatal(err)
	}
	meta := params.Meta
	if meta["yoloMode"] != true || meta["modelId"] != "explicit" || meta["clientIdentifier"] != "editor" ||
		meta["codeNavEnabled"] != true || meta["clientTerminal"] != true ||
		meta["clientFsRead"] != true || meta["clientFsWrite"] != false {
		t.Fatalf("meta=%+v", meta)
	}
	if _, exists := meta["autoMode"]; exists {
		t.Fatalf("yolo client must suppress autoMode: %+v", meta)
	}
	writeClientACP(t, connection, `{"jsonrpc":"2.0","id":2,"method":"session/set_model","params":{"modelId":"grok-next"}}`)
	_ = readACPLine(t, server)
	writeClientACP(t, connection, `{"jsonrpc":"2.0","id":3,"method":"session/new","params":{}}`)
	next := readACPLine(t, server)
	if json.Unmarshal(next.Params, &params) != nil || params.Meta["modelId"] != "grok-next" {
		t.Fatalf("next params=%s", next.Params)
	}
}

func TestServerRejectsUnsupportedHeadlessRegistration(t *testing.T) {
	root := shortLeaderTempDir(t)
	server, err := Start(context.Background(), ServerConfig{Root: root, NoExitOnDisconnect: true})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	connection, err := net.Dial("unix", filepath.Join(root, "leader.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if err := writeMessage(connection, map[string]any{
		"type": "register", "client_type": "remote", "mode": "headless", "capabilities": map[string]any{},
	}); err != nil {
		t.Fatal(err)
	}
	var response struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	}
	readClientMessage(t, connection, &response)
	if response.Type != "error" || response.Message != "headless relay mode is not supported" {
		t.Fatalf("response=%+v", response)
	}
}

func TestServerConcurrentClientLifecycle(t *testing.T) {
	root := shortLeaderTempDir(t)
	server, err := Start(context.Background(), ServerConfig{Root: root, NoExitOnDisconnect: true})
	if err != nil {
		t.Fatal(err)
	}
	server.MarkReady()
	path := filepath.Join(root, "leader.sock")
	var wait sync.WaitGroup
	for index := 0; index < 20; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			connection, dialErr := net.Dial("unix", path)
			if dialErr != nil {
				return
			}
			defer connection.Close()
			_ = writeMessage(connection, map[string]any{
				"type": "register", "client_type": "race", "mode": "stdio", "capabilities": map[string]any{},
			})
			var registered map[string]any
			_ = readMessage(connection, &registered)
			_ = writeMessage(connection, map[string]any{"type": "disconnect"})
		}()
	}
	wait.Wait()
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestServerDoesNotLeakDisconnectedSessionsAndAllowsReload(t *testing.T) {
	root := shortLeaderTempDir(t)
	server, err := Start(context.Background(), ServerConfig{Root: root, NoExitOnDisconnect: true})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	server.MarkReady()
	path := filepath.Join(root, "leader.sock")

	first := connectLeaderClient(t, path)
	writeClientACP(t, first, `{"jsonrpc":"2.0","id":1,"method":"session/new","params":{}}`)
	request := readACPLine(t, server)
	writeACPLine(t, server, rpcEnvelope{JSONRPC: "2.0", ID: request.ID, Result: json.RawMessage(`{"sessionId":"session-a"}`)})
	assertClientResponse(t, first, "1", "session-a")
	_ = writeMessage(first, map[string]any{"type": "disconnect"})
	_ = first.Close()
	time.Sleep(20 * time.Millisecond)

	second := connectLeaderClient(t, path)
	defer second.Close()
	writeACPLine(t, server, rpcEnvelope{
		JSONRPC: "2.0", Method: "session/update",
		Params: json.RawMessage(`{"sessionId":"session-a","update":{"sessionUpdate":"agent_message_chunk"}}`),
	})
	var message map[string]any
	_ = second.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	if err := readMessage(second, &message); err == nil {
		t.Fatalf("disconnected session notification leaked: %+v", message)
	}
	_ = second.SetReadDeadline(time.Time{})

	writeClientACP(t, second, `{"jsonrpc":"2.0","id":2,"method":"session/load","params":{"sessionId":"session-a"}}`)
	load := readACPLine(t, server)
	writeACPLine(t, server, rpcEnvelope{JSONRPC: "2.0", ID: load.ID, Result: json.RawMessage(`{"sessionId":"session-a"}`)})
	assertClientResponse(t, second, "2", "session-a")
	writeACPLine(t, server, rpcEnvelope{
		JSONRPC: "2.0", Method: "session/update",
		Params: json.RawMessage(`{"sessionId":"session-a","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"reloaded"}}}`),
	})
	var routed struct {
		Type    string `json:"type"`
		Payload string `json:"payload"`
	}
	readClientMessage(t, second, &routed)
	if !containsJSONText(routed.Payload, "reloaded") {
		t.Fatalf("routed=%+v", routed)
	}
}

func connectLeaderClient(t *testing.T, path string) *net.UnixConn {
	t.Helper()
	connection, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	if err := writeMessage(connection, map[string]any{
		"type": "register", "client_type": "test", "mode": "stdio", "capabilities": map[string]any{},
	}); err != nil {
		t.Fatal(err)
	}
	var registered struct {
		Type  string `json:"type"`
		Ready bool   `json:"ready"`
	}
	readClientMessage(t, connection, &registered)
	if registered.Type != "registered" || !registered.Ready {
		t.Fatalf("registered=%+v", registered)
	}
	return connection
}

func shortLeaderTempDir(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "gork-leader-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	return root
}

func writeClientACP(t *testing.T, connection net.Conn, payload string) {
	t.Helper()
	if err := writeMessage(connection, map[string]any{"type": "acp", "payload": payload}); err != nil {
		t.Fatal(err)
	}
}

func readACPLine(t *testing.T, server *Server) rpcEnvelope {
	t.Helper()
	var message rpcEnvelope
	decoder := json.NewDecoder(server)
	if err := decoder.Decode(&message); err != nil {
		t.Fatal(err)
	}
	return message
}

func writeACPLine(t *testing.T, server *Server, message rpcEnvelope) {
	t.Helper()
	data, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.Write(append(data, '\n')); err != nil {
		t.Fatal(err)
	}
}

func assertClientResponse(t *testing.T, connection net.Conn, id, sessionID string) {
	t.Helper()
	var message struct {
		Type    string `json:"type"`
		Payload string `json:"payload"`
	}
	readClientMessage(t, connection, &message)
	var response rpcEnvelope
	if err := json.Unmarshal([]byte(message.Payload), &response); err != nil {
		t.Fatal(err)
	}
	if message.Type != "acp" || string(response.ID) != id || responseSessionID(response.Result) != sessionID {
		t.Fatalf("message=%+v response=%+v", message, response)
	}
}

func readClientMessage(t *testing.T, connection net.Conn, value any) {
	t.Helper()
	_ = connection.SetReadDeadline(time.Now().Add(2 * time.Second))
	if err := readMessage(connection, value); err != nil {
		t.Fatal(err)
	}
	_ = connection.SetReadDeadline(time.Time{})
}

func containsJSONText(payload, text string) bool {
	var value any
	if json.Unmarshal([]byte(payload), &value) != nil {
		return false
	}
	data, _ := json.Marshal(value)
	return bytes.Contains(data, []byte(text))
}
