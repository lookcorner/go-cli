package api

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type tokenStreamer interface {
	StreamResponse(context.Context, ResponseRequest, func(string)) (StreamResult, error)
}

func TestClientsRefreshOnceAfterUnauthorized(t *testing.T) {
	tests := []struct {
		name      string
		path      string
		header    string
		success   string
		newClient func(*http.Client, TokenProvider) tokenStreamer
	}{
		{
			name: "responses", path: "/responses", header: "Authorization",
			success: `{"id":"resp-1","output":[]}`,
			newClient: func(httpClient *http.Client, provider TokenProvider) tokenStreamer {
				client := NewClient("https://example.invalid", "static", httpClient)
				client.SetTokenProvider(provider)
				return client
			},
		},
		{
			name: "chat", path: "/chat/completions", header: "Authorization",
			success: `{"id":"chat-1","choices":[{"message":{"role":"assistant","content":"ok"}}]}`,
			newClient: func(httpClient *http.Client, provider TokenProvider) tokenStreamer {
				client := NewChatClient("https://example.invalid", "static", httpClient)
				client.SetTokenProvider(provider)
				return client
			},
		},
		{
			name: "messages", path: "/messages", header: "x-api-key",
			success: `{"id":"msg-1","content":[{"type":"text","text":"ok"}]}`,
			newClient: func(httpClient *http.Client, provider TokenProvider) tokenStreamer {
				client := NewMessagesClient("https://example.invalid", "static", httpClient)
				client.SetTokenProvider(provider)
				return client
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requests := 0
			var tokens []string
			httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				requests++
				if request.URL.Path != test.path {
					t.Fatalf("request path=%q", request.URL.Path)
				}
				token := request.Header.Get(test.header)
				if test.header == "Authorization" {
					token = strings.TrimPrefix(token, "Bearer ")
				}
				tokens = append(tokens, token)
				status, body := http.StatusUnauthorized, `{"error":"expired"}`
				if requests == 2 {
					status, body = http.StatusOK, test.success
				}
				return &http.Response{
					StatusCode: status, Status: http.StatusText(status), Header: http.Header{"Content-Type": []string{"application/json"}},
					Body: io.NopCloser(strings.NewReader(body)), Request: request,
				}, nil
			})}
			var rejected []string
			provider := func(_ context.Context, rejectedToken string) (string, error) {
				rejected = append(rejected, rejectedToken)
				if rejectedToken == "" {
					return "old", nil
				}
				return "new", nil
			}
			result, err := test.newClient(httpClient, provider).StreamResponse(context.Background(), ResponseRequest{
				Model: "model", Input: []InputItem{{Type: "message", Role: "user", Content: "hello"}}, Stream: true,
			}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if requests != 2 || strings.Join(tokens, ",") != "old,new" || strings.Join(rejected, ",") != ",old" || result.ResponseID == "" {
				t.Fatalf("requests=%d tokens=%#v rejected=%#v result=%#v", requests, tokens, rejected, result)
			}
		})
	}
}

func TestStaticTokenDoesNotRetryUnauthorized(t *testing.T) {
	requests := 0
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		return &http.Response{
			StatusCode: http.StatusUnauthorized, Status: "401 Unauthorized", Header: make(http.Header),
			Body: io.NopCloser(strings.NewReader("expired")), Request: request,
		}, nil
	})}
	client := NewClient("https://example.invalid", "static", httpClient)
	if _, err := client.StreamResponse(context.Background(), ResponseRequest{Model: "model"}, nil); err == nil {
		t.Fatal("unauthorized response was accepted")
	}
	if requests != 1 {
		t.Fatalf("static token requests=%d", requests)
	}
}
