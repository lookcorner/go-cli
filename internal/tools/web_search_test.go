package tools

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestWebSearchUsesResponsesToolAndFormatsOutput(t *testing.T) {
	var request map[string]any
	client := &http.Client{Transport: webRoundTripFunc(func(incoming *http.Request) (*http.Response, error) {
		if incoming.Method != http.MethodPost || incoming.URL.Path != "/v1/responses" || incoming.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("unexpected request: %s %s headers=%v", incoming.Method, incoming.URL.Path, incoming.Header)
		}
		if err := json.NewDecoder(incoming.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		return &http.Response{
			StatusCode: http.StatusOK, Status: "200 OK", Header: make(http.Header), Request: incoming,
			Body: io.NopCloser(strings.NewReader(`{"output":[{"type":"web_search_call","status":"completed"},{"type":"message","content":[{"type":"output_text","text":"First result."},{"type":"output_text","text":" Second result."}]}]}`)),
		}, nil
	})}

	tool := NewWebSearchTool("https://api.example/v1", "secret", "search-model", client)
	output, err := tool.Execute(context.Background(), json.RawMessage(`{"query":"latest Go release","allowed_domains":["go.dev"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if output != "Web search results for: \"latest Go release\"\n\nFirst result. Second result." {
		t.Fatalf("unexpected output: %q", output)
	}
	if request["model"] != "search-model" || request["input"] != "latest Go release" || request["store"] != false {
		t.Fatalf("unexpected request body: %#v", request)
	}
	tools := request["tools"].([]any)
	filters := tools[0].(map[string]any)["filters"].(map[string]any)
	domains := filters["allowed_domains"].([]any)
	if len(domains) != 1 || domains[0] != "go.dev" {
		t.Fatalf("allowed domains were not forwarded: %#v", request)
	}
}

func TestWebSearchHandlesEmptyAndErrorResponses(t *testing.T) {
	status := http.StatusOK
	client := &http.Client{Transport: webRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		body := `{"error":{"message":"search unavailable"}}`
		if status == http.StatusOK {
			body = `{"output":[]}`
		}
		return &http.Response{
			StatusCode: status, Status: http.StatusText(status), Header: make(http.Header), Request: request,
			Body: io.NopCloser(strings.NewReader(body)),
		}, nil
	})}
	tool := NewWebSearchTool("https://api.example", "secret", "model", client)
	output, err := tool.Execute(context.Background(), json.RawMessage(`{"query":"nothing"}`))
	if err != nil || output != "Web search results for: \"nothing\"\n\nNo search results found." {
		t.Fatalf("unexpected empty result: output=%q err=%v", output, err)
	}
	status = http.StatusBadGateway
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"query":"failure"}`)); err == nil {
		t.Fatal("expected upstream error")
	}
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"query":""}`)); err == nil {
		t.Fatal("expected empty query error")
	}
}
