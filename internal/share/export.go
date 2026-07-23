package share

import (
	"encoding/json"

	"github.com/lookcorner/go-cli/internal/session"
)

type Update struct {
	Timestamp int64          `json:"timestamp"`
	Method    string         `json:"method"`
	Params    map[string]any `json:"params"`
}

func Updates(path, sessionID string) ([]Update, error) {
	events, err := session.Events(path)
	if err != nil {
		return nil, err
	}
	updates := make([]Update, 0, len(events))
	appendStandard := func(timestamp int64, update map[string]any) {
		updates = append(updates, Update{Timestamp: timestamp, Method: "session/update", Params: map[string]any{"sessionId": sessionID, "update": update}})
	}
	appendExtension := func(timestamp int64, update map[string]any, meta map[string]any) {
		params := map[string]any{"sessionId": sessionID, "update": update}
		if len(meta) > 0 {
			params["_meta"] = meta
		}
		updates = append(updates, Update{Timestamp: timestamp, Method: "_x.ai/session/update", Params: params})
	}

	for _, event := range events {
		timestamp := event.Time.UnixMilli()
		switch event.Kind {
		case "user_prompt":
			var data struct {
				Text      string            `json:"text"`
				Content   []session.Content `json:"content"`
				Synthetic bool              `json:"synthetic"`
			}
			if decodeEvent(event.Data, &data) != nil || data.Synthetic {
				continue
			}
			if len(data.Content) == 0 {
				if data.Text != "" {
					appendStandard(timestamp, messageChunk("user_message_chunk", map[string]any{"type": "text", "text": data.Text}))
				}
				continue
			}
			hasText := false
			for _, part := range data.Content {
				hasText = hasText || part.Type == "text"
			}
			if data.Text != "" && !hasText {
				appendStandard(timestamp, messageChunk("user_message_chunk", map[string]any{"type": "text", "text": data.Text}))
			}
			for _, part := range data.Content {
				part, materializeErr := session.MaterializeContent(path, part)
				if materializeErr != nil {
					return nil, materializeErr
				}
				content := map[string]any{"type": part.Type}
				if part.Type == "text" {
					content["text"] = part.Text
				} else if part.Data == "" {
					content["uri"] = part.URI
				} else {
					content["data"], content["mimeType"] = part.Data, part.MimeType
				}
				appendStandard(timestamp, messageChunk("user_message_chunk", content))
			}
		case "model_response":
			var data struct {
				Text string `json:"text"`
			}
			if decodeEvent(event.Data, &data) == nil && data.Text != "" {
				appendStandard(timestamp, messageChunk("agent_message_chunk", map[string]any{"type": "text", "text": data.Text}))
			}
		case "tool_call":
			var data struct {
				CallID    string          `json:"call_id"`
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			if decodeEvent(event.Data, &data) == nil && data.CallID != "" {
				update := map[string]any{"sessionUpdate": "tool_call", "toolCallId": data.CallID, "title": data.Name, "kind": toolKind(data.Name), "status": "in_progress"}
				if len(data.Arguments) > 0 {
					update["rawInput"] = data.Arguments
				}
				appendStandard(timestamp, update)
			}
		case "tool_result":
			var data struct {
				CallID string `json:"call_id"`
				Output string `json:"output"`
				Failed bool   `json:"failed"`
			}
			if decodeEvent(event.Data, &data) == nil && data.CallID != "" {
				status := "completed"
				if data.Failed {
					status = "failed"
				}
				appendStandard(timestamp, map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": data.CallID, "status": status, "rawOutput": data.Output})
			}
		case "session_mode":
			var data struct {
				ModeID string `json:"mode_id"`
			}
			if decodeEvent(event.Data, &data) == nil && data.ModeID != "" {
				appendStandard(timestamp, map[string]any{"sessionUpdate": "current_mode_update", "currentModeId": data.ModeID})
			}
		case "subagent_spawned", "subagent_finished", "task_backgrounded", "task_completed":
			update, ok := event.Data.(map[string]any)
			if !ok {
				continue
			}
			update = cloneMap(update)
			if _, exists := update["sessionUpdate"]; !exists {
				update["sessionUpdate"] = event.Kind
			}
			appendExtension(timestamp, update, nil)
		case "xai_session_notification":
			params, ok := event.Data.(map[string]any)
			if !ok {
				continue
			}
			update, _ := params["update"].(map[string]any)
			meta, _ := params["_meta"].(map[string]any)
			if update != nil {
				appendExtension(timestamp, update, meta)
			}
		}
	}
	return updates, nil
}

func decodeEvent(data any, target any) error {
	encoded, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return json.Unmarshal(encoded, target)
}

func messageChunk(kind string, content map[string]any) map[string]any {
	return map[string]any{"sessionUpdate": kind, "content": content}
}

func cloneMap(value map[string]any) map[string]any {
	cloned := make(map[string]any, len(value)+1)
	for key, item := range value {
		cloned[key] = item
	}
	return cloned
}

func toolKind(name string) string {
	switch name {
	case "read_file", "list_dir", "list_files", "get_task_output", "get_background_command_output", "lsp":
		return "read"
	case "grep", "search_files":
		return "search"
	case "write_file", "edit_file", "search_replace":
		return "edit"
	case "run_terminal_cmd", "shell", "monitor", "start_background_command", "kill_task", "kill_background_command":
		return "execute"
	case "todo_write", "update_goal", "enter_plan_mode", "exit_plan_mode", "ask_user_question":
		return "think"
	default:
		return "other"
	}
}
