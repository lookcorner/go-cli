package api

import (
	"testing"

	"github.com/lookcorner/go-cli/internal/session"
)

func TestLocalBackendsRebuildVisibleHistoryAfterRewind(t *testing.T) {
	messages := []session.Message{
		{Role: "user", Text: "first"},
		{Role: "assistant", Text: "answer"},
		{Role: "user", Content: []session.Content{
			{Type: "text", Text: "image prompt"},
			{Type: "image", MimeType: "image/png", Data: "cG5n"},
		}},
	}
	chat := &ChatClient{}
	chat.RewindHistory(messages)
	if len(chat.history) != 3 || chat.history[0].Role != "user" || chat.history[1].Role != "assistant" {
		t.Fatalf("unexpected chat rewind history: %#v", chat.history)
	}
	parts, ok := chat.history[2].Content.([]chatContentPart)
	if !ok || len(parts) != 2 || parts[1].ImageURL.URL != "data:image/png;base64,cG5n" {
		t.Fatalf("chat rewind lost multimodal content: %#v", chat.history[2].Content)
	}

	anthropic := &MessagesClient{}
	anthropic.RewindHistory(messages)
	if len(anthropic.history) != 3 || anthropic.history[1].Content[0].Text != "answer" {
		t.Fatalf("unexpected messages rewind history: %#v", anthropic.history)
	}
	source := anthropic.history[2].Content[1].Source
	if source == nil || source.Type != "base64" || source.MediaType != "image/png" || source.Data != "cG5n" {
		t.Fatalf("messages rewind lost image content: %#v", anthropic.history[2])
	}
}
