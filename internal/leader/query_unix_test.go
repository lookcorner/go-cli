//go:build unix

package leader

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestQueryUsesUnixSocketProtocol(t *testing.T) {
	root, err := os.MkdirTemp("", "gork-leader-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	socketPath := filepath.Join(root, "leader.sock")
	listener, err := net.Listen("unix", socketPath)
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
		var register map[string]any
		if err := readMessage(connection, &register); err != nil {
			serverDone <- err
			return
		}
		if err := writeMessage(connection, map[string]any{
			"type": "registered", "client_id": 8, "ready": false,
			"leader_protocol_version": 1,
			"leader_capabilities":     map[string]any{"control_v1": true},
		}); err != nil {
			serverDone <- err
			return
		}
		if err := writeMessage(connection, map[string]any{"type": "leader_ready"}); err != nil {
			serverDone <- err
			return
		}
		var control map[string]any
		if err := readMessage(connection, &control); err != nil {
			serverDone <- err
			return
		}
		serverDone <- writeMessage(connection, map[string]any{
			"type": "control_result", "request_id": "1",
			"result": map[string]any{"Ok": map[string]any{
				"type": "leader_info", "pid": 321, "socket_path": socketPath,
				"lock_path":     filepath.Join(filepath.Dir(socketPath), "leader.lock"),
				"ws_url_suffix": "", "leader_protocol_version": 1,
				"leader_binary_version": "2.0.0",
			}},
		})
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	result, err := query(ctx, socketPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
	if result.ClientID != 8 || result.Info.PID != 321 || result.Info.SocketPath != socketPath {
		t.Fatalf("result=%+v", result)
	}
}
