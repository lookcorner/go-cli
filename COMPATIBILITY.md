# Gork Build compatibility

This file tracks behavioral compatibility against
`thedavidweng/gork-build@b06c5682c48247f77900d62753349835c5ce1f3f`.

Status values: **done**, **partial**, **planned**.

| Area | Status | Current behavior / remaining work |
| --- | --- | --- |
| Responses API streaming | done | SSE text deltas, terminal response IDs, function calls and JSON fallback |
| Headless agent loop | done | Multi-step model/tool loop with cancellation and a configurable limit |
| Workspace file tools | partial | Gork-compatible `read_file`, `list_dir`, `grep`, and `search_replace` schemas cover text/PPTX reads, PDF text extraction and 150-DPI page-image rendering with page ranges, validated PNG/JPEG/GIF/WebP reads with native Responses/Chat/Anthropic image content, negative offsets, Gitignore-aware bounded directory trees with large-subtree extension summaries, ripgrep filters, atomic create/overwrite, exact/all replacement, CRLF preservation and safe Unicode typography fallback; PDF image rendering uses Poppler's `pdftoppm` |
| Tool permissions | partial | Prompt/auto/deny modes, repeatable CLI rules and compatible `[permission].rules` for any/bash/edit/read/grep/MCP/WebFetch (glob and domain) with deny→ask→allow precedence work; managed system policy remains |
| Shell execution | partial | Gork-compatible tools support foreground exit status, background timeout/lifecycle, multi-task wait/poll, process groups, cleanup, and replayed cwd/environment/functions/aliases; shell-option parity, interactive PTY sessions and cgroups remain |
| Session persistence | partial | Durable JSONL events, latest/path resume, completed-turn response-ID continuation and checkpoint-aligned local transcript reconstruction work; rewind, branching and migrations remain |
| Configuration | partial | Gork-compatible `~/.grok/config.toml`, model selection/custom providers, MCP/LSP tables, legacy JSON, environment and CLI layers work; full UI/toolset fields, requirements and managed config remain |
| Authentication | partial | API-key bearer auth; xAI OAuth/device flow and refresh storage remain |
| Chat Completions backend | done | Streaming text, incremental tool calls, tool-result messages, image content and process-local multi-turn history are covered by protocol tests |
| Anthropic Messages backend | done | Streaming content blocks, incremental JSON tool input, tool results, base64 image blocks, headers and process-local history are covered by protocol tests |
| Interactive UI | partial | Multi-turn REPL plus Bubble Tea v2 full-screen streaming, Unicode input, scrolling, cancellation, status and approval UI work; Markdown/media, dashboard panes, mouse and advanced editing remain |
| MCP | partial | Stdio, Streamable HTTP and standalone SSE lifecycle, endpoint/origin validation, session/protocol headers, JSON/SSE responses, version negotiation, paginated live tool/resource/prompt discovery, resource reads, prompt rendering, multimodal tool results, tool list-change reload, shutdown, and permission-gated stdio/SSE sampling work; Streamable HTTP reverse sampling and resource subscriptions remain |
| ACP | partial | Protocol v1 JSON-RPC stdio supports independent sessions, persistent list/filter metadata, load with completed text/image-history replay, resume without replay, prompt continuation, text/base64/remote-image prompt blocks, embedded text context, streamed text/tool lifecycle updates with native image/PDF page content, cancellation, close, client-provided stdio/HTTP/SSE MCP servers and tool-correlated permission requests; modes, audio prompts and ACP client mode remain |
| Web search | partial | `web_search` calls the Responses API native search tool with optional domain filters and reference-compatible prompt output, supports `[models].web_search` selection plus environment overrides, and falls back to the active Responses model; dynamic credential refresh remains |
| Web fetch | partial | Permission-gated public HTTP(S) fetches enforce 2,000-character URLs, single-label rejection, HTTPS upgrades, same-host bounded redirects, DNS/dial SSRF checks, bounded bodies, structured HTML-to-Markdown conversion, relative links, executable/embed stripping and binary rejection; caching, overflow artifacts and media downloads remain |
| LSP tools | partial | Framed stdio JSON-RPC, initialization/shutdown, initialization options, dynamic settings and section queries, document sync, hover, definitions, references, symbols and published diagnostics work; progress, apply-edit and server-specific adapters remain |
| Planning / goals | partial | `todo_write`, active-goal state, `update_goal`, bounded autonomous `--goal` continuation and terminal completion/block handling work; plan-mode UI, scheduled continuation and classifier verification remain |
| Skills / AGENTS.md | partial | Compatible home-level and Git-root-to-CWD scoped instruction/rules and skill discovery, deeper-scope precedence, `GROK_HOME`, source-labelled injection, Git-native instruction ignore filtering, normalized names/descriptions/scalar-or-list paths, lazy loading, `paths`-gated activation, direct/deep dynamic discovery, and per-vendor TOML/environment gates work; remote compatibility flags and background skill watching remain |
| Git / worktrees / hunk tracking | partial | Git tracked/staged/untracked hunk parsing, stable IDs, file summaries, file-tool agent attribution, ACP read queries and safe hunk/file/all actions work; persisted linked/standalone/git lifecycle, sync/async fork creation, dirty-state copying, status notifications, overwrite/merge conflict reporting, safe removal, GC and registry management, local session resolution/resume, historical HEAD restore with stash protection and idempotent local rehydrate work; mixed-source per-hunk attribution, prompt-index turn actions, remote archive/full working-state rehydrate, snapshot disposal and optimized CoW copying remain |
| Subagents | planned | Coordinator, roster and activity events remain |
| Memory / compaction | partial | Responses, Chat Completions and Anthropic usage accounting, compatible 85% threshold resolution, successful-summary fresh-chain compaction, manual `/compact` in REPL/TUI, TUI context status and configurable old-tool-result pruning work; two-pass prefire, memory flush and local memory index remain |
| OS sandbox | planned | Landlock/Seatbelt, child seccomp and network policy remain |
| Workspace server | planned | Hub server, previews, remote workspaces and supervision remain |
| Markdown/media/Mermaid | planned | Terminal rendering and inline media remain |
| Telemetry/privacy hard-offs | done | No analytics, research uploads, auto-update or retention opt-in code exists |

Compatibility is verified with Go unit/integration tests and will additionally
use captured protocol fixtures from the Rust implementation. A status is only
changed to **done** after its relevant compatibility tests exist.
