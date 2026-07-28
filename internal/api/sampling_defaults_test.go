package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

type samplingDefaultStreamer interface {
	Streamer
	SetSamplingDefaults(SamplingDefaults)
}

func TestInferenceClientsApplySamplingDefaults(t *testing.T) {
	tests := []struct {
		name      string
		maxKey    string
		success   string
		streamKey bool
		newClient func(*http.Client) samplingDefaultStreamer
	}{
		{name: "responses", maxKey: "max_output_tokens", streamKey: true, success: `{"id":"resp-1","output":[]}`, newClient: func(client *http.Client) samplingDefaultStreamer {
			return NewClient("https://example.invalid", "key", client)
		}},
		{name: "chat", maxKey: "max_completion_tokens", success: `{"id":"chat-1","choices":[{"message":{"content":"ok"}}]}`, newClient: func(client *http.Client) samplingDefaultStreamer {
			return NewChatClient("https://example.invalid", "key", client)
		}},
		{name: "messages", maxKey: "max_tokens", success: `{"id":"msg-1","content":[{"type":"text","text":"ok"}]}`, newClient: func(client *http.Client) samplingDefaultStreamer {
			return NewMessagesClient("https://example.invalid", "key", client)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				var body map[string]any
				if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
					return nil, err
				}
				if body["temperature"] != 0.7 || body["top_p"] != 0.9 || body[test.maxKey] != float64(2048) {
					t.Fatalf("sampling body=%#v", body)
				}
				if got, exists := body["stream_tool_calls"]; exists != test.streamKey || test.streamKey && got != true {
					t.Fatalf("stream_tool_calls=%#v exists=%v body=%#v", got, exists, body)
				}
				return &http.Response{
					StatusCode: http.StatusOK, Status: "200 OK",
					Header: http.Header{"Content-Type": []string{"application/json"}},
					Body:   io.NopCloser(strings.NewReader(test.success)), Request: request,
				}, nil
			})}
			temperature, topP, maxTokens, streamTools := 0.7, 0.9, uint32(2048), true
			client := test.newClient(httpClient)
			client.SetSamplingDefaults(SamplingDefaults{
				Temperature: &temperature, TopP: &topP,
				MaxCompletionTokens: &maxTokens, StreamToolCalls: &streamTools,
			})
			if _, err := client.StreamResponse(context.Background(), ResponseRequest{Model: "model"}, nil); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestExplicitSamplingRequestOverridesDefaults(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			return nil, err
		}
		if body["temperature"] != 0.1 || body["top_p"] != 0.2 || body["max_output_tokens"] != float64(64) {
			t.Fatalf("sampling body=%#v", body)
		}
		return &http.Response{
			StatusCode: http.StatusOK, Status: "200 OK",
			Header: http.Header{"Content-Type": []string{"application/json"}},
			Body:   io.NopCloser(strings.NewReader(`{"id":"resp-1","output":[]}`)), Request: request,
		}, nil
	})}
	defaultTemperature, defaultTopP, maxTokens := 0.7, 0.9, uint32(2048)
	client := NewClient("https://example.invalid", "key", httpClient)
	client.SetSamplingDefaults(SamplingDefaults{
		Temperature: &defaultTemperature, TopP: &defaultTopP, MaxCompletionTokens: &maxTokens,
	})
	temperature, topP := 0.1, 0.2
	if _, err := client.StreamResponse(context.Background(), ResponseRequest{
		Model: "model", Temperature: &temperature, TopP: &topP, MaxOutputTokens: 64,
	}, nil); err != nil {
		t.Fatal(err)
	}
}
