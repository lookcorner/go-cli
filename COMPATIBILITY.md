# Gork Build compatibility

This file tracks behavioral compatibility against
`thedavidweng/gork-build@b06c5682c48247f77900d62753349835c5ce1f3f`.

Status values: **done**, **partial**, **planned**.

| Area | Status | Current behavior / remaining work |
| --- | --- | --- |
| Responses API streaming | done | SSE text deltas, terminal response IDs, function calls and JSON fallback |
| Headless agent loop | done | Multi-step model/tool loop with cancellation and a configurable limit |
| Workspace file tools | partial | Gork-compatible `read_file`, `list_dir`, `grep`, and `search_replace` schemas cover text reads with negative offsets, ripgrep filters, atomic create/overwrite and exact/all replacement; document/image formats, full gitignore traversal and Unicode-normalized editing remain |
| Tool permissions | partial | Prompt/auto/deny modes, repeatable CLI rules and compatible `[permission].rules` for any/bash/edit/read/grep/MCP with deny→ask→allow precedence work; WebFetch/domain rules and managed system policy remain |
| Shell execution | partial | Gork-compatible tools support foreground exit status, background timeout/lifecycle, multi-task wait/poll, process groups, cleanup, and replayed cwd/environment/functions/aliases; shell-option parity, interactive PTY sessions and cgroups remain |
| Session persistence | partial | Durable JSONL events, latest/path resume, completed-turn response-ID continuation and checkpoint-aligned local transcript reconstruction work; rewind, branching and migrations remain |
| Configuration | partial | Gork-compatible `~/.grok/config.toml`, model selection/custom providers, MCP/LSP tables, legacy JSON, environment and CLI layers work; full UI/toolset fields, requirements and managed config remain |
| Authentication | partial | API-key bearer auth; xAI OAuth/device flow and refresh storage remain |
| Chat Completions backend | done | Streaming text, incremental tool calls, tool-result messages and process-local multi-turn history are covered by protocol tests |
| Anthropic Messages backend | done | Streaming content blocks, incremental JSON tool input, tool results, headers and process-local history are covered by protocol tests |
| Interactive UI | partial | Multi-turn REPL plus Bubble Tea v2 full-screen streaming, Unicode input, scrolling, cancellation, status and approval UI work; Markdown/media, dashboard panes, mouse and advanced editing remain |
| MCP | partial | Stdio and Streamable HTTP lifecycle, session/protocol headers, JSON/SSE responses, version negotiation, paginated tool/resource/prompt discovery, resource reads, prompt rendering, tool calls, tool list-change reload and shutdown work; standalone SSE transport, sampling, resource subscriptions and prompt/resource list reload remain |
| ACP | partial | Protocol v1 JSON-RPC stdio supports independent sessions, persistent list/filter metadata, load with completed text-history replay, resume without replay, prompt continuation, streamed text/tool lifecycle updates, cancellation, close, client-provided stdio MCP servers, embedded text context and tool-correlated permission requests; modes, non-text history replay, image/audio prompts and ACP client mode remain |
| LSP tools | partial | Framed stdio JSON-RPC, initialization/shutdown, document sync, hover, definitions, references, symbols and published diagnostics work; dynamic configuration, progress, apply-edit and server-specific adapters remain |
| Planning / goals | partial | `todo_write`, active-goal state, `update_goal`, bounded autonomous `--goal` continuation and terminal completion/block handling work; plan-mode UI, scheduled continuation and classifier verification remain |
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
