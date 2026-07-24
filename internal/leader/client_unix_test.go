//go:build unix

package leader

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestConnectHonorsContextDuringRegistration(t *testing.T) {
	root := shortLeaderTempDir(t)
	listener, err := net.Listen("unix", SocketPath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		connection, err := listener.Accept()
		if err == nil {
			defer connection.Close()
			time.Sleep(time.Second)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := Connect(ctx, SocketPath(root), Registration{ClientType: "test"}); err == nil {
		t.Fatal("connect unexpectedly succeeded")
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("connect ignored context deadline: %s", elapsed)
	}
}

func TestACPMessageDirectionDetection(t *testing.T) {
	if _, ok := acpRequestID(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`); !ok {
		t.Fatal("request detection is incorrect")
	}
	if _, ok := acpRequestID(`{"jsonrpc":"2.0","id":1,"result":{}}`); ok {
		t.Fatal("response detected as a request")
	}
	if _, ok := acpResponseID(`{"jsonrpc":"2.0","id":1,"result":{}}`); !ok {
		t.Fatal("response detection is incorrect")
	}
	if _, ok := acpResponseID(`{"jsonrpc":"2.0","id":1,"method":"permission/request"}`); ok {
		t.Fatal("request detected as a response")
	}
}

func TestClientWaitsForReadyAndBridgesACP(t *testing.T) {
	root := shortLeaderTempDir(t)
	server, err := Start(context.Background(), ServerConfig{Root: root, NoExitOnDisconnect: true})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	connected := make(chan *Client, 1)
	connectErr := make(chan error, 1)
	go func() {
		client, err := Connect(context.Background(), SocketPath(root), Registration{ClientType: "test"})
		if err != nil {
			connectErr <- err
			return
		}
		connected <- client
	}()
	time.Sleep(25 * time.Millisecond)
	server.MarkReady()

	var client *Client
	select {
	case client = <-connected:
	case err := <-connectErr:
		t.Fatal(err)
	case <-time.After(time.Second):
		t.Fatal("client did not finish registration")
	}
	defer client.Close()

	inputReader, inputWriter := io.Pipe()
	outputReader, outputWriter := io.Pipe()
	serveDone := make(chan error, 1)
	go func() { serveDone <- client.Serve(context.Background(), inputReader, outputWriter) }()

	request := `{"jsonrpc":"2.0","id":7,"method":"initialize","params":{}}`
	if _, err := io.WriteString(inputWriter, request+"\n"); err != nil {
		t.Fatal(err)
	}
	routed := readACPLine(t, server)
	if routed.Method != "initialize" {
		t.Fatalf("routed=%+v", routed)
	}
	writeACPLine(t, server, rpcEnvelope{
		JSONRPC: "2.0", ID: routed.ID, Result: json.RawMessage(`{"protocolVersion":1}`),
	})
	line, err := bufio.NewReader(outputReader).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	var response rpcEnvelope
	if json.Unmarshal([]byte(line), &response) != nil || string(response.ID) != "7" {
		t.Fatalf("response=%s", line)
	}
	_ = inputWriter.Close()
	select {
	case err := <-serveDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("client did not disconnect at input EOF")
	}
}

func TestConnectOrSpawnCoordinatesConcurrentFollowers(t *testing.T) {
	root := shortLeaderTempDir(t)
	socketPath := SocketPath(root)
	var spawnCount atomic.Int32
	var serverMu sync.Mutex
	var server *Server
	spawn := func() error {
		spawnCount.Add(1)
		started, err := Start(context.Background(), ServerConfig{Root: root, NoExitOnDisconnect: true})
		if err != nil {
			return err
		}
		started.MarkReady()
		serverMu.Lock()
		server = started
		serverMu.Unlock()
		return nil
	}

	start := make(chan struct{})
	clients := make(chan *Client, 2)
	errs := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			client, err := ConnectOrSpawn(ctx, socketPath, Registration{ClientType: "test"}, spawn)
			if err != nil {
				errs <- err
				return
			}
			clients <- client
		}()
	}
	close(start)
	var connected []*Client
	for range 2 {
		select {
		case client := <-clients:
			connected = append(connected, client)
		case err := <-errs:
			t.Fatal(err)
		case <-time.After(4 * time.Second):
			t.Fatal("followers did not connect")
		}
	}
	for _, client := range connected {
		_ = client.Close()
	}
	if spawnCount.Load() != 1 {
		t.Fatalf("spawn count=%d", spawnCount.Load())
	}
	serverMu.Lock()
	defer serverMu.Unlock()
	if server != nil {
		_ = server.Close()
	}
}
