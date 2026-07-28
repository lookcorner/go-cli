package remote

import (
	"os"
	"strings"
)

const (
	// ChatModeEnv is the process-wide gateway chat-mode flag (`--chat`).
	ChatModeEnv = "GROK_CHAT_MODE"

	// ChatModeLocalBuildRefusal matches the reference pager copy.
	ChatModeLocalBuildRefusal = "cannot open a local Build session while --chat is active; resume a conversation or start a new chat"
	// ChatModeForkConflict matches the reference pager copy.
	ChatModeForkConflict = "--fork-session is not supported with --chat"
	// ChatModeLeaderConflict matches the reference pager copy.
	ChatModeLeaderConflict = "gateway chat mode (--chat) cannot run with leader mode; pass --no-leader or disable [cli] use_leader in config"
)

// ProcessChatModeEnabled reports sticky process-wide chat mode.
func ProcessChatModeEnabled() bool {
	return envTruthy(ChatModeEnv)
}

// EnableProcessChatMode sets GROK_CHAT_MODE=1 for the current process.
func EnableProcessChatMode() error {
	return os.Setenv(ChatModeEnv, "1")
}

// ChatModeFlagConflict returns a user-facing error when Build-only flags
// combine with --chat.
func ChatModeFlagConflict(chatMode, forkSession bool) string {
	if chatMode && forkSession {
		return ChatModeForkConflict
	}
	return ""
}

// ChatModeConflictsWithLeader reports sticky --chat + leader is invalid.
func ChatModeConflictsWithLeader(chatMode, useLeader bool) bool {
	return chatMode && useLeader
}

// ConversationsLaneActive mirrors Rust conversations_lane_active for Go builds:
// opt-in via GROK_SESSION_LIST_CONVERSATIONS or process-wide GROK_CHAT_MODE.
func ConversationsLaneActive() bool {
	return envTruthy("GROK_SESSION_LIST_CONVERSATIONS") || ProcessChatModeEnabled()
}

func envTruthy(key string) bool {
	value := strings.TrimSpace(strings.ToLower(os.Getenv(key)))
	switch value {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
