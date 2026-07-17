# Gork Build compatibility

This file tracks behavioral compatibility against
`thedavidweng/gork-build@b06c5682c48247f77900d62753349835c5ce1f3f`.

Status values: **done**, **partial**, **planned**.

| Area | Status | Current behavior / remaining work |
| --- | --- | --- |
| Responses API streaming | done | SSE text deltas, terminal response IDs, function calls and JSON fallback |
| Headless agent loop | done | Multi-step model/tool loop with cancellation and a configurable limit |
| Workspace file tools | done | Read, list, regex search, atomic write and exact-text edit |
| Tool permissions | partial | Prompt/auto/deny modes are present; command-prefix and managed policy rules remain |
| Shell execution | partial | Timeout, combined capped output and workspace CWD; persistent PTY/background process support remains |
| Session persistence | partial | Durable JSONL events, latest/path resume and completed-turn response-ID continuation work; local transcript reconstruction, rewind, branching and migrations remain |
| Configuration | partial | JSON, environment and CLI layers; TOML compatibility and managed config remain |
| Authentication | partial | API-key bearer auth; xAI OAuth/device flow and refresh storage remain |
| Chat Completions backend | done | Streaming text, incremental tool calls, tool-result messages and process-local multi-turn history are covered by protocol tests |
| Anthropic Messages backend | done | Streaming content blocks, incremental JSON tool input, tool results, headers and process-local history are covered by protocol tests |
| Interactive UI | partial | Multi-turn REPL plus Bubble Tea v2 full-screen streaming, Unicode input, scrolling, cancellation, status and approval UI work; Markdown/media, dashboard panes, mouse and advanced editing remain |
| MCP | partial | Stdio lifecycle, version negotiation, paginated tool discovery/calls and shutdown work; Streamable HTTP, resources, prompts, sampling and list-change reload remain |
| ACP | planned | Agent Client Protocol server/client modes remain |
| LSP tools | planned | Process lifecycle, JSON-RPC and diagnostics remain |
| Skills / AGENTS.md | partial | Compatible root instruction/rules discovery, source-labelled injection, user/workspace skill discovery and lazy `skill` loading work; gitignore-aware nested discovery and conditional skills remain |
| Git / worktrees / hunk tracking | planned | Worktree lifecycle and edit attribution remain |
| Subagents | planned | Coordinator, roster and activity events remain |
| Memory / compaction | planned | Context accounting, compaction and local memory index remain |
| OS sandbox | planned | Landlock/Seatbelt, child seccomp and network policy remain |
| Workspace server | planned | Hub server, previews, remote workspaces and supervision remain |
| Markdown/media/Mermaid | planned | Terminal rendering and inline media remain |
| Telemetry/privacy hard-offs | done | No analytics, research uploads, auto-update or retention opt-in code exists |

Compatibility is verified with Go unit/integration tests and will additionally
use captured protocol fixtures from the Rust implementation. A status is only
changed to **done** after its relevant compatibility tests exist.
