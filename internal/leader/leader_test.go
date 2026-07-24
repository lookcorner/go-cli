package leader

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverClassifiesLeaderCandidates(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "leader.lock"), []byte("42\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "leader-dev.lock"), []byte("bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "leader-dev.sock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	descriptors, err := discover(context.Background(), root, func(_ context.Context, path string) (QueryResult, error) {
		if filepath.Base(path) == "leader-dev.sock" {
			return QueryResult{Info: Info{PID: 99}}, nil
		}
		return QueryResult{}, io.EOF
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(descriptors) != 2 {
		t.Fatalf("descriptors=%+v", descriptors)
	}
	if descriptors[0].State != StateStale || descriptors[0].PID() == nil || *descriptors[0].PID() != 42 {
		t.Fatalf("production=%+v", descriptors[0])
	}
	if descriptors[1].State != StateReachable || descriptors[1].LiveInfo == nil || descriptors[1].LiveInfo.PID != 99 {
		t.Fatalf("dev=%+v", descriptors[1])
	}
}

func TestSelectUsesProductionOrPID(t *testing.T) {
	production := Descriptor{State: StateReachable, LiveInfo: &Info{PID: 10}}
	dev := Descriptor{State: StateReachable, WSURLSuffix: "-dev", LiveInfo: &Info{PID: 20}}
	selected, err := Select([]Descriptor{dev, production}, nil)
	if err != nil || selected.LiveInfo.PID != 10 {
		t.Fatalf("selected=%+v err=%v", selected, err)
	}
	pid := uint32(20)
	selected, err = Select([]Descriptor{production, dev}, &pid)
	if err != nil || selected.LiveInfo.PID != 20 {
		t.Fatalf("selected=%+v err=%v", selected, err)
	}
}

func TestQueryStreamRegistrationAndInfo(t *testing.T) {
	client, server := pipePair()
	done := make(chan error, 1)
	go func() {
		defer server.Close()
		var register map[string]any
		if err := readMessage(server, &register); err != nil {
			done <- err
			return
		}
		if err := writeMessage(server, map[string]any{
			"type": "registered", "client_id": 7, "ready": true,
			"leader_protocol_version": 1,
			"leader_capabilities":     map[string]any{"control_v1": true},
		}); err != nil {
			done <- err
			return
		}
		var control map[string]any
		if err := readMessage(server, &control); err != nil {
			done <- err
			return
		}
		done <- writeMessage(server, map[string]any{
			"type": "control_result", "request_id": "1",
			"result": map[string]any{"Ok": map[string]any{
				"type": "leader_info", "pid": 123, "socket_path": "/tmp/leader.sock",
				"lock_path": "/tmp/leader.lock", "ws_url_suffix": "",
				"leader_protocol_version": 1, "leader_binary_version": "1.2.3",
			}},
		})
	}()
	result, err := queryStream(context.Background(), client)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if result.ClientID != 7 || result.Info.PID != 123 || result.Info.LeaderBinaryVersion != "1.2.3" {
		t.Fatalf("result=%+v", result)
	}
}

func TestQueryStreamRejectsUnsupportedProtocol(t *testing.T) {
	client, server := pipePair()
	go func() {
		defer server.Close()
		var register map[string]any
		_ = readMessage(server, &register)
		_ = writeMessage(server, map[string]any{
			"type": "registered", "client_id": 7, "ready": true,
			"leader_protocol_version": 2,
			"leader_capabilities":     map[string]any{"control_v1": true},
		})
	}()
	if _, err := queryStream(context.Background(), client); !errors.Is(err, ErrUnsupportedProtocol) {
		t.Fatalf("err=%v", err)
	}
}

func TestReadMessageRejectsOversizedFrame(t *testing.T) {
	var frame [4]byte
	binary.BigEndian.PutUint32(frame[:], maxMessageBytes+1)
	if err := readMessage(bytes.NewReader(frame[:]), &map[string]any{}); err == nil {
		t.Fatal("oversized frame was accepted")
	}
}

func pipePair() (io.ReadWriteCloser, io.ReadWriteCloser) {
	return net.Pipe()
}
