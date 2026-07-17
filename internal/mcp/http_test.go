package mcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

func TestEventStreamDispatchesNotificationBeforeResponse(t *testing.T) {
	stream := "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/tools/list_changed\"}\n\n" +
		"data: {\"jsonrpc\":\"2.0\",\"id\":7,\"result\":{}}\n\n"
	var notification string
	message, err := readMCPEventStream(strings.NewReader(stream), func(method string) { notification = method })
	if err != nil {
		t.Fatal(err)
	}
	if notification != "notifications/tools/list_changed" || string(message.ID) != "7" {
		t.Fatalf("unexpected stream result: notification=%q message=%#v", notification, message)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func TestStreamableHTTPLifecycleJSONAndSSE(t *testing.T) {
	var mu sync.Mutex
	var methods []string
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		mu.Lock()
		defer mu.Unlock()
		if request.Header.Get("Authorization") != "Bearer fixture" {
			t.Fatalf("missing configured header: %#v", request.Header)
		}
		if request.Method == http.MethodDelete {
			if request.Header.Get("Mcp-Session-Id") != "session-fixture" {
				t.Fatalf("DELETE missing session ID")
			}
			methods = append(methods, "DELETE")
			return httpResponse(http.StatusNoContent, "", ""), nil
		}
		var rpc struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
			Params struct {
				Capabilities map[string]any `json:"capabilities"`
			} `json:"params"`
		}
		if err := json.NewDecoder(request.Body).Decode(&rpc); err != nil {
			t.Fatal(err)
		}
		methods = append(methods, rpc.Method)
		if rpc.Method != "initialize" && request.Header.Get("Mcp-Session-Id") != "session-fixture" {
			t.Fatalf("request %s missing session ID", rpc.Method)
		}
		if rpc.Method == "initialize" && request.Header.Get("MCP-Protocol-Version") != "" {
			t.Fatalf("initialize sent a protocol header before negotiation")
		}
		if rpc.Method != "initialize" && request.Header.Get("MCP-Protocol-Version") != protocolVersion {
			t.Fatalf("request %s missing negotiated protocol header", rpc.Method)
		}
		switch rpc.Method {
		case "initialize":
			if _, advertised := rpc.Params.Capabilities["sampling"]; advertised {
				t.Fatal("Streamable HTTP advertised sampling without a reverse channel")
			}
			response := httpResponse(http.StatusOK, "application/json", rpcResult(rpc.ID, map[string]any{
				"protocolVersion": protocolVersion,
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "http-fixture", "version": "1.0"},
			}))
			response.Header.Set("Mcp-Session-Id", "session-fixture")
			return response, nil
		case "notifications/initialized":
			return httpResponse(http.StatusAccepted, "", ""), nil
		case "tools/list":
			body := "event: message\ndata: " + rpcResult(rpc.ID, map[string]any{"tools": []any{map[string]any{
				"name": "echo", "description": "echo", "inputSchema": map[string]any{"type": "object"},
			}}}) + "\n\n"
			return httpResponse(http.StatusOK, "text/event-stream", body), nil
		case "tools/call":
			return httpResponse(http.StatusOK, "application/json", rpcResult(rpc.ID, map[string]any{
				"content": []any{map[string]any{"type": "text", "text": "echoed"}},
			})), nil
		default:
			t.Fatalf("unexpected method %s", rpc.Method)
			return nil, nil
		}
	})
	client, initialized, err := StartHTTP(context.Background(), HTTPConfig{
		Name: "fixture", URL: "https://mcp.example/rpc",
		Headers: map[string]string{"Authorization": "Bearer fixture"},
		Client:  &http.Client{Transport: transport},
		Sampling: func(context.Context, SamplingRequest) (SamplingResult, error) {
			return SamplingResult{}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if initialized.ServerInfo.Name != "http-fixture" {
		t.Fatalf("unexpected initialize result: %#v", initialized)
	}
	remoteTools, err := client.ListTools(context.Background())
	if err != nil || len(remoteTools) != 1 || remoteTools[0].Name != "echo" {
		t.Fatalf("unexpected tools=%#v err=%v", remoteTools, err)
	}
	result, err := client.CallTool(context.Background(), "echo", map[string]any{"message": "hello"})
	if err != nil || len(result.Content) != 1 || result.Content[0].Text != "echoed" {
		t.Fatalf("unexpected tool result=%#v err=%v", result, err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{"initialize", "notifications/initialized", "tools/list", "tools/call", "DELETE"}
	if strings.Join(methods, ",") != strings.Join(want, ",") {
		t.Fatalf("unexpected lifecycle: %#v", methods)
	}
}

func httpResponse(status int, contentType string, body string) *http.Response {
	response := &http.Response{
		StatusCode: status, Status: http.StatusText(status), Header: make(http.Header),
		Body: io.NopCloser(strings.NewReader(body)),
	}
	if contentType != "" {
		response.Header.Set("Content-Type", contentType)
	}
	return response
}

func rpcResult(id any, result any) string {
	encoded, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	return string(encoded)
}
