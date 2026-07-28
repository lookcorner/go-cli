package api

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type extraHeaderStreamer interface {
	Streamer
	SetExtraHeaders(map[string]string)
}

func TestInferenceClientsApplyExtraHeaders(t *testing.T) {
	tests := []struct {
		name      string
		authName  string
		authValue string
		success   string
		newClient func(*http.Client) extraHeaderStreamer
	}{
		{name: "responses", authName: "Authorization", authValue: "Custom responses", success: `{"id":"resp-1","output":[]}`, newClient: func(client *http.Client) extraHeaderStreamer {
			return NewClient("https://example.invalid", "key", client)
		}},
		{name: "chat", authName: "Authorization", authValue: "Custom chat", success: `{"id":"chat-1","choices":[{"message":{"content":"ok"}}]}`, newClient: func(client *http.Client) extraHeaderStreamer {
			return NewChatClient("https://example.invalid", "key", client)
		}},
		{name: "messages", authName: "x-api-key", authValue: "custom-messages", success: `{"id":"msg-1","content":[{"type":"text","text":"ok"}]}`, newClient: func(client *http.Client) extraHeaderStreamer {
			return NewMessagesClient("https://example.invalid", "key", client)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				if request.Header.Get("X-Trace") != "session" || request.Header.Get("User-Agent") != "custom-agent" || request.Header.Get(test.authName) != test.authValue {
					t.Fatalf("headers=%#v", request.Header)
				}
				return &http.Response{
					StatusCode: http.StatusOK, Status: "200 OK",
					Header: http.Header{"Content-Type": []string{"application/json"}},
					Body:   io.NopCloser(strings.NewReader(test.success)), Request: request,
				}, nil
			})}
			client := test.newClient(httpClient)
			client.SetExtraHeaders(map[string]string{"X-Trace": "session", "user-agent": "custom-agent", test.authName: test.authValue})
			if _, err := client.StreamResponse(context.Background(), ResponseRequest{Model: "model"}, nil); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDynamicCredentialsOverrideExtraAuthenticationHeaders(t *testing.T) {
	tests := []struct {
		name      string
		authName  string
		authValue string
		success   string
		newClient func(*http.Client, TokenProvider) extraHeaderStreamer
	}{
		{name: "responses", authName: "Authorization", authValue: "Bearer dynamic", success: `{"id":"resp-1","output":[]}`, newClient: func(client *http.Client, provider TokenProvider) extraHeaderStreamer {
			streamer := NewClient("https://example.invalid", "static", client)
			streamer.SetTokenProvider(provider)
			return streamer
		}},
		{name: "chat", authName: "Authorization", authValue: "Bearer dynamic", success: `{"id":"chat-1","choices":[{"message":{"content":"ok"}}]}`, newClient: func(client *http.Client, provider TokenProvider) extraHeaderStreamer {
			streamer := NewChatClient("https://example.invalid", "static", client)
			streamer.SetTokenProvider(provider)
			return streamer
		}},
		{name: "messages", authName: "x-api-key", authValue: "dynamic", success: `{"id":"msg-1","content":[{"type":"text","text":"ok"}]}`, newClient: func(client *http.Client, provider TokenProvider) extraHeaderStreamer {
			streamer := NewMessagesClient("https://example.invalid", "static", client)
			streamer.SetTokenProvider(provider)
			return streamer
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				if request.Header.Get(test.authName) != test.authValue {
					t.Fatalf("headers=%#v", request.Header)
				}
				return &http.Response{
					StatusCode: http.StatusOK, Status: "200 OK",
					Header: http.Header{"Content-Type": []string{"application/json"}},
					Body:   io.NopCloser(strings.NewReader(test.success)), Request: request,
				}, nil
			})}
			provider := func(context.Context, string) (string, error) { return "dynamic", nil }
			client := test.newClient(httpClient, provider)
			client.SetExtraHeaders(map[string]string{test.authName: "stale-extra"})
			if _, err := client.StreamResponse(context.Background(), ResponseRequest{Model: "model"}, nil); err != nil {
				t.Fatal(err)
			}
		})
	}
}
