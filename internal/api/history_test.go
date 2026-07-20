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

func TestLocalBackendCompactionClonesIsolateHistory(t *testing.T) {
	chat := &ChatClient{baseURL: "https://chat.example", apiKey: "key", history: []chatMessage{{Role: "user", Content: "original"}}}
	chatWithHistory := chat.CloneForCompaction(true).(*ChatClient)
	chatWithoutHistory := chat.CloneForCompaction(false).(*ChatClient)
	chatWithHistory.history[0].Content = "changed"
	if chat.history[0].Content != "original" || len(chatWithoutHistory.history) != 0 || chatWithHistory.baseURL != chat.baseURL {
		t.Fatalf("chat clones leaked history: source=%#v with=%#v without=%#v", chat.history, chatWithHistory.history, chatWithoutHistory.history)
	}

	messages := &MessagesClient{history: []messagesMessage{{Role: "user", Content: []messagesBlock{{Type: "text", Text: "original"}}}}}
	messagesWithHistory := messages.CloneForCompaction(true).(*MessagesClient)
	messagesWithoutHistory := messages.CloneForCompaction(false).(*MessagesClient)
	messagesWithHistory.history[0].Content[0].Text = "changed"
	if messages.history[0].Content[0].Text != "original" || len(messagesWithoutHistory.history) != 0 {
		t.Fatalf("messages clones leaked history: source=%#v with=%#v without=%#v", messages.history, messagesWithHistory.history, messagesWithoutHistory.history)
	}
}
