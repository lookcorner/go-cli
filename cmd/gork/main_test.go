package main

import (
	"context"
	"strings"
	"testing"

	"github.com/lookcorner/go-cli/internal/api"
	"github.com/lookcorner/go-cli/internal/config"
	"github.com/lookcorner/go-cli/internal/mcp"
	"github.com/lookcorner/go-cli/internal/tools"
)

type samplingStreamer struct {
	request api.ResponseRequest
}

func (s *samplingStreamer) StreamResponse(_ context.Context, request api.ResponseRequest, _ func(string)) (api.StreamResult, error) {
	s.request = request
	return api.StreamResult{Text: "sampled response"}, nil
}

func TestRunMCPSamplingMapsConversation(t *testing.T) {
	streamer := &samplingStreamer{}
	result, err := runMCPSampling(context.Background(), streamer, "sample-model", mcp.SamplingRequest{
		SystemPrompt: "Be concise", MaxTokens: 128,
		Messages: []mcp.SamplingMessage{
			{Role: "user", Content: mcp.SamplingContent{Type: "text", Text: "question"}},
			{Role: "assistant", Content: mcp.SamplingContent{Type: "text", Text: "prior answer"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Role != "assistant" || result.Content.Text != "sampled response" || result.Model != "sample-model" || result.StopReason != "endTurn" {
		t.Fatalf("unexpected sampling result: %#v", result)
	}
	request := streamer.request
	if request.Model != "sample-model" || request.Instructions != "Be concise" || request.MaxOutputTokens != 128 || len(request.Input) != 2 {
		t.Fatalf("unexpected model request: %#v", request)
	}
	if request.Input[0].Role != "user" || request.Input[0].Content != "question" || request.Input[1].Role != "assistant" {
		t.Fatalf("sampling messages were not preserved: %#v", request.Input)
	}
}

func TestRunMCPSamplingRejectsUnsupportedContent(t *testing.T) {
	_, err := runMCPSampling(context.Background(), &samplingStreamer{}, "model", mcp.SamplingRequest{
		Messages: []mcp.SamplingMessage{{Role: "user", Content: mcp.SamplingContent{Type: "audio"}}},
	})
	if err == nil {
		t.Fatal("unsupported sampling content was accepted")
	}
}

func TestMCPSamplingRequiresApproval(t *testing.T) {
	handler := newMCPSamplingHandler(config.Config{}, tools.PromptApprover{Mode: tools.PermissionDeny}, "fixture")
	_, err := handler(context.Background(), mcp.SamplingRequest{
		MaxTokens: 1, Messages: []mcp.SamplingMessage{{Role: "user", Content: mcp.SamplingContent{Type: "text", Text: "private context"}}},
	})
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("unexpected approval error: %v", err)
	}
}
