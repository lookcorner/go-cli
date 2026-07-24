//go:build unix

package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAgentLeaderRoutesACPInitialize(t *testing.T) {
	home, err := os.MkdirTemp("/tmp", "gork-agent-leader-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	t.Setenv("GROK_HOME", home)
	t.Setenv("GORK_API_KEY", "test-key")
	root := t.TempDir()
	done := make(chan error, 1)
	var stderr bytes.Buffer
	go func() {
		done <- runAgent([]string{"--model", "test-model", "--workspace", root, "leader"}, strings.NewReader(""), io.Discard, &stderr)
	}()

	socketPath := filepath.Join(home, "leader.sock")
	var connection net.Conn
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		select {
		case runErr := <-done:
			t.Fatalf("agent leader exited before listening: %v\n%s", runErr, stderr.String())
		default:
		}
		connection, err = net.Dial("unix", socketPath)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("%v\n%s", err, stderr.String())
	}
	defer connection.Close()
	writeLeaderFrame(t, connection, map[string]any{
		"type": "register", "client_type": "test", "mode": "stdio", "capabilities": map[string]any{},
	})
	var registered struct {
		Type  string `json:"type"`
		Ready bool   `json:"ready"`
	}
	readLeaderFrame(t, connection, &registered)
	if registered.Type != "registered" {
		t.Fatalf("registered=%+v", registered)
	}
	if !registered.Ready {
		var ready struct {
			Type string `json:"type"`
		}
		if err := decodeLeaderFrame(connection, &ready); err != nil {
			select {
			case runErr := <-done:
				t.Fatalf("leader closed before ready: %v\n%s", runErr, stderr.String())
			case <-time.After(time.Second):
				t.Fatalf("leader closed before ready: %v\n%s", err, stderr.String())
			}
		}
		if ready.Type != "leader_ready" {
			t.Fatalf("ready=%+v", ready)
		}
	}

	writeLeaderFrame(t, connection, map[string]any{
		"type": "acp", "payload": `{"jsonrpc":"2.0","id":7,"method":"initialize","params":{"protocolVersion":1}}`,
	})
	var wrapper struct {
		Type    string `json:"type"`
		Payload string `json:"payload"`
	}
	readLeaderFrame(t, connection, &wrapper)
	var response struct {
		ID     int `json:"id"`
		Result struct {
			ProtocolVersion int `json:"protocolVersion"`
		} `json:"result"`
	}
	if wrapper.Type != "acp" || json.Unmarshal([]byte(wrapper.Payload), &response) != nil ||
		response.ID != 7 || response.Result.ProtocolVersion != 1 {
		t.Fatalf("wrapper=%+v response=%+v", wrapper, response)
	}
	writeLeaderFrame(t, connection, map[string]any{"type": "disconnect"})
	_ = connection.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("agent leader did not exit after its last client disconnected")
	}
}

func writeLeaderFrame(t *testing.T, writer io.Writer, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(data)))
	if _, err := writer.Write(append(length[:], data...)); err != nil {
		t.Fatal(err)
	}
}

func readLeaderFrame(t *testing.T, reader io.Reader, value any) {
	t.Helper()
	if err := decodeLeaderFrame(reader, value); err != nil {
		t.Fatal(err)
	}
}

func decodeLeaderFrame(reader io.Reader, value any) error {
	var length [4]byte
	if _, err := io.ReadFull(reader, length[:]); err != nil {
		return err
	}
	data := make([]byte, binary.BigEndian.Uint32(length[:]))
	if _, err := io.ReadFull(reader, data); err != nil {
		return err
	}
	if err := json.Unmarshal(data, value); err != nil {
		return err
	}
	return nil
}
