package acp

import (
	"encoding/json"
	"fmt"
	"strings"
)

type queuedPrompt struct {
	incoming   message
	request    promptRequest
	id         string
	version    uint64
	owner      string
	lastEditor string
	text       string
	sendNow    bool
}

type queueUpdateRequest struct {
	SessionID        string   `json:"sessionId"`
	ID               string   `json:"id"`
	ExpectedVersion  uint64   `json:"expectedVersion"`
	OrderedIDs       []string `json:"orderedIds"`
	NewText          *string  `json:"newText"`
	Owner            string   `json:"owner"`
	ClientIdentifier string   `json:"clientIdentifier"`
}

func promptID(meta map[string]any) string {
	id, _ := meta["promptId"].(string)
	return id
}

func promptOwner(meta map[string]any) string {
	owner, _ := meta["owner"].(string)
	if owner == "" {
		owner, _ = meta["clientIdentifier"].(string)
	}
	return owner
}

func promptSendNow(meta map[string]any) bool {
	sendNow, _ := meta["sendNow"].(bool)
	return sendNow
}

func (s *Server) queuePrompt(current *session, incoming message, request *promptRequest, text string) bool {
	if request.Meta == nil {
		request.Meta = make(map[string]any)
	}
	id := promptID(request.Meta)
	if id == "" {
		id = fmt.Sprintf("gork-prompt-%d", s.nextRequest.Add(1))
		request.Meta["promptId"] = id
	}

	current.mu.Lock()
	if current.closed {
		current.mu.Unlock()
		s.respondError(incoming.ID, -32000, "session is closed")
		return true
	}
	if current.startingPromptID == id {
		current.startingPromptID = ""
		current.mu.Unlock()
		return false
	}
	if !current.running && current.startingPromptID == "" {
		current.mu.Unlock()
		return false
	}
	item := queuedPrompt{
		incoming: incoming, request: *request, id: id, owner: promptOwner(request.Meta),
		text: queueDisplayText(request.Prompt, text), sendNow: promptSendNow(request.Meta),
	}
	if item.sendNow {
		insertQueuedPrompt(&current.promptQueue, item)
	} else {
		current.promptQueue = append(current.promptQueue, item)
	}
	if item.sendNow && current.cancel != nil && !goalActive(current) {
		current.cancel()
	}
	current.mu.Unlock()
	s.broadcastQueue(current)
	return true
}

func queueDisplayText(blocks []promptBlock, fallback string) string {
	for _, block := range blocks {
		if block.Type == "text" && block.Meta != nil {
			if text, _ := block.Meta["displayText"].(string); strings.TrimSpace(text) != "" {
				return text
			}
		}
	}
	return fallback
}

func goalActive(current *session) bool {
	if current.runner == nil || current.runner.Tools == nil {
		return false
	}
	status := current.runner.Tools.GoalSnapshot().Status
	return status == "active" || status == "verifying"
}

func (s *Server) markRunningPrompt(current *session, id string) {
	current.mu.Lock()
	if current.running {
		current.runningPromptID = id
		current.mu.Unlock()
		s.broadcastQueue(current)
		return
	}
	current.runningPromptID = ""
	current.mu.Unlock()
	s.startNext(current)
}

func (s *Server) startNext(current *session) {
	current.mu.Lock()
	if current.closed || current.running || current.startingPromptID != "" {
		current.mu.Unlock()
		return
	}
	notify := current.runningPromptID != "" || len(current.promptQueue) > 0
	current.runningPromptID = ""
	if len(current.promptQueue) == 0 {
		current.mu.Unlock()
		if notify {
			s.broadcastQueue(current)
		}
		s.startNextWake(current)
		return
	}
	item := current.promptQueue[0]
	current.promptQueue = current.promptQueue[1:]
	current.startingPromptID = item.id
	current.runningPromptID = item.id
	current.mu.Unlock()
	s.broadcastQueue(current)
	s.handlePromptRequest(current.ctx, item.incoming, current, item.request)
}

func (s *Server) broadcastQueue(current *session) {
	if s.output == nil {
		return
	}
	current.mu.Lock()
	entries := make([]map[string]any, len(current.promptQueue))
	for index, item := range current.promptQueue {
		entry := map[string]any{"id": item.id, "version": item.version, "kind": "prompt", "text": item.text, "position": index}
		if item.owner != "" {
			entry["owner"] = item.owner
		}
		if item.lastEditor != "" {
			entry["lastEditor"] = item.lastEditor
		}
		entries[index] = entry
	}
	params := map[string]any{"sessionId": current.id, "entries": entries}
	if current.runningPromptID != "" {
		params["runningPromptId"] = current.runningPromptID
	}
	current.mu.Unlock()
	s.write(map[string]any{"jsonrpc": "2.0", "method": "x.ai/queue/changed", "params": params})
}

func (s *Server) handleQueueUpdate(incoming message) {
	var request queueUpdateRequest
	if json.Unmarshal(incoming.Params, &request) != nil || request.SessionID == "" {
		return
	}
	current := s.lookupSession(request.SessionID)
	if current == nil {
		return
	}
	owner := request.Owner
	if owner == "" {
		owner = request.ClientIdentifier
	}
	var removed []queuedPrompt
	changed := false
	current.mu.Lock()
	switch incoming.Method {
	case "x.ai/queue/remove":
		if index := queuedPromptIndex(current.promptQueue, request.ID); index >= 0 {
			item := current.promptQueue[index]
			if item.version == request.ExpectedVersion && (owner == "" || item.owner == owner) {
				removed = append(removed, item)
				current.promptQueue = append(current.promptQueue[:index], current.promptQueue[index+1:]...)
			}
		}
	case "x.ai/queue/reorder":
		ordered := make([]queuedPrompt, 0, len(current.promptQueue))
		seen := make(map[string]bool, len(request.OrderedIDs))
		for _, id := range request.OrderedIDs {
			if seen[id] {
				continue
			}
			seen[id] = true
			if index := queuedPromptIndex(current.promptQueue, id); index >= 0 {
				ordered = append(ordered, current.promptQueue[index])
			}
		}
		for _, item := range current.promptQueue {
			if !seen[item.id] {
				ordered = append(ordered, item)
			}
		}
		current.promptQueue = ordered
	case "x.ai/queue/clear":
		kept := current.promptQueue[:0]
		for _, item := range current.promptQueue {
			if owner == "" || item.owner == owner {
				removed = append(removed, item)
			} else {
				kept = append(kept, item)
			}
		}
		current.promptQueue = kept
	case "x.ai/queue/edit":
		if request.NewText != nil && strings.TrimSpace(*request.NewText) != "" {
			if index := queuedPromptIndex(current.promptQueue, request.ID); index >= 0 {
				editQueuedPrompt(&current.promptQueue[index], *request.NewText, owner)
				changed = true
			}
		}
	case "x.ai/queue/interject":
		if index := queuedPromptIndex(current.promptQueue, request.ID); index >= 0 {
			item := current.promptQueue[index]
			if item.version == request.ExpectedVersion && (owner == "" || item.owner == owner) {
				if request.NewText != nil && strings.TrimSpace(*request.NewText) != "" {
					editQueuedPrompt(&item, *request.NewText, owner)
				}
				if current.running {
					current.promptQueue = append(current.promptQueue[:index], current.promptQueue[index+1:]...)
					item.sendNow = true
					insertQueuedPrompt(&current.promptQueue, item)
					if current.cancel != nil && !goalActive(current) {
						current.cancel()
					}
				} else {
					current.promptQueue[index] = item
				}
			}
		}
	}
	current.mu.Unlock()
	for _, item := range removed {
		s.respond(item.incoming.ID, map[string]any{"stopReason": "cancelled"})
	}
	if changed || incoming.Method != "x.ai/queue/edit" {
		s.broadcastQueue(current)
	}
}

func queuedPromptIndex(queue []queuedPrompt, id string) int {
	for index := range queue {
		if queue[index].id == id {
			return index
		}
	}
	return -1
}

func insertQueuedPrompt(queue *[]queuedPrompt, item queuedPrompt) {
	index := 0
	for index < len(*queue) && (*queue)[index].sendNow {
		index++
	}
	*queue = append(*queue, queuedPrompt{})
	copy((*queue)[index+1:], (*queue)[index:])
	(*queue)[index] = item
}

func editQueuedPrompt(item *queuedPrompt, text, editor string) {
	blocks := []promptBlock{{Type: "text", Text: text}}
	for _, block := range item.request.Prompt {
		if block.Type != "text" {
			blocks = append(blocks, block)
		}
	}
	item.request.Prompt = blocks
	item.text = text
	item.lastEditor = editor
	if item.version != ^uint64(0) {
		item.version++
	}
}
