package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStandaloneSSELifecycle(t *testing.T) {
	streamReader, streamWriter := io.Pipe()
	streamClosed := make(chan struct{})
	streamBody := &notifyingReadCloser{ReadCloser: streamReader, closed: streamClosed}
	var streamMu sync.Mutex
	writeEvent := func(event string) {
		streamMu.Lock()
		defer streamMu.Unlock()
		_, _ = fmt.Fprintf(streamWriter, "event: message\ndata: %s\n\n", event)
	}
	var methods []string
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("Authorization") != "Bearer fixture" {
			t.Errorf("request missing configured header: %#v", request.Header)
		}
		if request.Method == http.MethodGet {
			go func() {
				_, _ = fmt.Fprint(streamWriter, "event: endpoint\ndata: /messages\n\n")
			}()
			return &http.Response{
				StatusCode: http.StatusOK, Status: "200 OK",
				Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: streamBody, Request: request,
			}, nil
		}
		if request.Method != http.MethodPost || request.URL.Path != "/messages" || request.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("unexpected SSE request: %s %s %#v", request.Method, request.URL, request.Header)
		}
		var rpc struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		if err := json.NewDecoder(request.Body).Decode(&rpc); err != nil {
			return nil, err
		}
		methods = append(methods, rpc.Method)
		switch rpc.Method {
		case "initialize":
			writeEvent(rpcResult(rpc.ID, map[string]any{
				"protocolVersion": protocolVersion,
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "sse-fixture", "version": "1.0"},
			}))
		case "tools/list":
			writeEvent(`{"jsonrpc":"2.0","method":"notifications/tools/list_changed"}`)
			writeEvent(rpcResult(rpc.ID, map[string]any{"tools": []any{map[string]any{
				"name": "echo", "description": "echo", "inputSchema": map[string]any{"type": "object"},
			}}}))
		case "tools/call":
			writeEvent(rpcResult(rpc.ID, map[string]any{"content": []any{map[string]any{"type": "text", "text": "echoed"}}}))
		}
		return httpResponse(http.StatusAccepted, "", ""), nil
	})
	client, initialized, err := StartSSE(context.Background(), HTTPConfig{
		Name: "fixture", URL: "https://mcp.example/sse",
		Headers: map[string]string{"Authorization": "Bearer fixture"}, Client: &http.Client{Transport: transport},
	})
	if err != nil {
		t.Fatal(err)
	}
	if initialized.ServerInfo.Name != "sse-fixture" {
		t.Fatalf("unexpected initialize result: %#v", initialized)
	}
	notified := make(chan string, 1)
	client.SetNotificationHandler(func(method string) { notified <- method })
	remoteTools, err := client.ListTools(context.Background())
	if err != nil || len(remoteTools) != 1 || remoteTools[0].Name != "echo" {
		t.Fatalf("unexpected tools=%#v err=%v", remoteTools, err)
	}
	select {
	case method := <-notified:
		if method != "notifications/tools/list_changed" {
			t.Fatalf("unexpected notification %q", method)
		}
	case <-time.After(time.Second):
		t.Fatal("SSE notification was not dispatched")
	}
	result, err := client.CallTool(context.Background(), "echo", map[string]any{"message": "hello"})
	if err != nil || len(result.Content) != 1 || result.Content[0].Text != "echoed" {
		t.Fatalf("unexpected tool result=%#v err=%v", result, err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-streamClosed:
	case <-time.After(time.Second):
		t.Fatal("closing SSE client did not close the event stream")
	}
	want := "initialize,notifications/initialized,tools/list,tools/call"
	if strings.Join(methods, ",") != want {
		t.Fatalf("unexpected lifecycle: %#v", methods)
	}
}

type notifyingReadCloser struct {
	io.ReadCloser
	closed chan struct{}
	once   sync.Once
}

func (r *notifyingReadCloser) Close() error {
	err := r.ReadCloser.Close()
	r.once.Do(func() { close(r.closed) })
	return err
}

func TestStandaloneSSERejectsCrossOriginPostEndpoint(t *testing.T) {
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return httpResponse(http.StatusOK, "text/event-stream", "event: endpoint\ndata: https://attacker.invalid/messages\n\n"), nil
	})
	_, _, err := StartSSE(context.Background(), HTTPConfig{
		Name: "fixture", URL: "https://mcp.example/sse", Client: &http.Client{Transport: transport},
	})
	if err == nil || !strings.Contains(err.Error(), "configured origin") {
		t.Fatalf("cross-origin endpoint was not rejected: %v", err)
	}
}
