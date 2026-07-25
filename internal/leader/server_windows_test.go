//go:build windows

package leader

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPipeName(t *testing.T) {
	t.Parallel()
	first := pipeName(filepath.Join(`C:\Users\X\AppData\grok`, "leader.sock"))
	if !strings.HasPrefix(first, `\\.\pipe\gork-go-leader-`) {
		t.Fatalf("pipeName=%q", first)
	}
	if again := pipeName(filepath.Join(`C:\Users\X\AppData\grok`, "leader.sock")); again != first {
		t.Fatalf("not deterministic: %q vs %q", first, again)
	}
	if upper := pipeName(`C:\USERS\X\APPDATA\GROK\LEADER.SOCK`); upper != first {
		t.Fatalf("case variant mapped differently: %q vs %q", first, upper)
	}
	if other := pipeName(filepath.Join(`C:\other`, "leader.sock")); other == first {
		t.Fatal("different roots must map to different pipes")
	}
}

func TestWindowsServerDiscoveryConnectAndCleanup(t *testing.T) {
	root := t.TempDir()
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
	if descriptors[0].PIDFromLock == nil || *descriptors[0].PIDFromLock != uint32(os.Getpid()) {
		t.Fatalf("lock PID not readable while the leader holds it: %+v", descriptors[0])
	}

	client, err := Connect(context.Background(), filepath.Join(root, "leader.sock"), Registration{ClientType: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.write(map[string]any{"type": "ping"}); err != nil {
		t.Fatal(err)
	}
	var pong struct {
		Type string `json:"type"`
	}
	readClient(t, client.stream, &pong)
	if pong.Type != "pong" {
		t.Fatalf("pong=%+v", pong)
	}

	if err := client.write(map[string]any{
		"type": "acp", "payload": `{"jsonrpc":"2.0","id":1,"method":"session/new","params":{}}`,
	}); err != nil {
		t.Fatal(err)
	}
	var request rpcEnvelope
	if err := json.NewDecoder(server).Decode(&request); err != nil {
		t.Fatal(err)
	}
	if request.Method != "session/new" {
		t.Fatalf("request=%+v", request)
	}
	response, _ := json.Marshal(rpcEnvelope{JSONRPC: "2.0", ID: request.ID, Result: json.RawMessage(`{"sessionId":"s-1"}`)})
	if _, err := server.Write(append(response, '\n')); err != nil {
		t.Fatal(err)
	}
	var routed struct {
		Type    string `json:"type"`
		Payload string `json:"payload"`
	}
	readClient(t, client.stream, &routed)
	var reply rpcEnvelope
	if err := json.Unmarshal([]byte(routed.Payload), &reply); err != nil || string(reply.ID) != "1" ||
		responseSessionID(reply.Result) != "s-1" {
		t.Fatalf("routed=%+v", routed)
	}
	_ = client.Close()

	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"leader.sock", "leader.lock"} {
		if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Fatalf("%s was not cleaned up: %v", name, err)
		}
	}
	restarted, err := Start(context.Background(), ServerConfig{Root: root})
	if err != nil {
		t.Fatalf("leader did not release its lock: %v", err)
	}
	_ = restarted.Close()
}

func TestWindowsServerDisconnectShutdown(t *testing.T) {
	root := t.TempDir()
	server, err := Start(context.Background(), ServerConfig{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := dial(context.Background(), filepath.Join(root, "leader.sock"))
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
	readClient(t, connection, &registered)
	if registered.Type != "registered" || registered.Ready {
		t.Fatalf("registered=%+v", registered)
	}
	server.MarkReady()
	var ready struct {
		Type string `json:"type"`
	}
	readClient(t, connection, &ready)
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

func readClient(t *testing.T, stream io.ReadWriteCloser, value any) {
	t.Helper()
	if connection, ok := stream.(interface{ SetDeadline(time.Time) error }); ok {
		_ = connection.SetDeadline(time.Now().Add(2 * time.Second))
		defer connection.SetDeadline(time.Time{})
	}
	if err := readMessage(stream, value); err != nil {
		t.Fatal(err)
	}
}
