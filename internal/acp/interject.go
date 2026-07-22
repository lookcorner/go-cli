package acp

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/lookcorner/go-cli/internal/agent"
	"github.com/lookcorner/go-cli/internal/api"
	sessionlog "github.com/lookcorner/go-cli/internal/session"
)

func (s *Server) handleInterject(incoming message) {
	var params struct {
		SessionID      string        `json:"sessionId"`
		Text           *string       `json:"text"`
		InterjectionID string        `json:"interjectionId"`
		Content        []promptBlock `json:"content"`
	}
	if json.Unmarshal(incoming.Params, &params) != nil || params.SessionID == "" || params.Text == nil {
		s.respondError(incoming.ID, -32602, "sessionId and text are required")
		return
	}
	text, images, err := interjectionContent(*params.Text, params.Content)
	if err != nil {
		s.respondError(incoming.ID, -32602, err.Error())
		return
	}
	current := s.lookupSession(params.SessionID)
	if current == nil {
		s.respondError(incoming.ID, -32602, "session not found: "+params.SessionID)
		return
	}
	current.mu.Lock()
	if current.closed {
		current.mu.Unlock()
		s.respondError(incoming.ID, -32603, "session is closed")
		return
	}
	if current.running {
		current.runner.QueueInterjection(text, images)
	} else {
		current.interjectionQueue = append(current.interjectionQueue, agent.Interjection{Text: text, Content: images})
	}
	current.updated = time.Now().UTC()
	current.mu.Unlock()

	payload := map[string]any{"sessionId": params.SessionID, "text": text}
	if params.InterjectionID != "" {
		payload["interjectionId"] = params.InterjectionID
	}
	s.write(map[string]any{"jsonrpc": "2.0", "method": "x.ai/session/interjection", "params": payload})
	s.respond(incoming.ID, map[string]any{"result": map[string]any{"status": "queued"}})
	s.startNext(current)
}

func interjectionContent(fallback string, blocks []promptBlock) (string, []api.ContentPart, error) {
	text := fallback
	overridden := false
	var images []api.ContentPart
	for _, block := range blocks {
		switch block.Type {
		case "text":
			if strings.TrimSpace(block.Text) != "" && !overridden {
				text = block.Text
				overridden = true
			}
		case "image":
			imageURL, err := promptImageURL(block)
			if err != nil {
				return "", nil, err
			}
			if block.Data != "" {
				data, _ := base64.StdEncoding.DecodeString(block.Data)
				if !sessionlog.ValidImage(block.MimeType, data) {
					return "", nil, fmt.Errorf("image data does not match mime type %q", block.MimeType)
				}
			}
			images = append(images, api.ContentPart{Type: "input_image", ImageURL: imageURL})
		case "audio", "resource", "resource_link":
		default:
			return "", nil, fmt.Errorf("unsupported interjection content type %q", block.Type)
		}
	}
	return text, images, nil
}
