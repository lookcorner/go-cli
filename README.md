# Gork Go

Gork Go is a Go reimplementation of [Gork Build](https://github.com/thedavidweng/gork-build),
the privacy-oriented community build of the Grok Build coding agent.

The project is under active compatibility development. The current runtime is
a usable headless coding agent with a Responses-compatible streaming client,
function-tool loop, workspace confinement, explicit mutation approval and
local JSONL session records. See [COMPATIBILITY.md](COMPATIBILITY.md) for the
feature-by-feature status.

## Build

Go 1.25 or newer is required.

```sh
go test ./...
go build -o gork ./cmd/gork
```

The headless runtime uses the Go standard library; the full-screen UI pins
[Bubble Tea v2](https://github.com/charmbracelet/bubbletea) and its terminal
runtime dependencies.

## Configure

Use environment variables for credentials:

```sh
export XAI_API_KEY="..."
export GORK_MODEL="a-responses-compatible-model"
```

The default API base URL is `https://api.x.ai/v1`. Override it with
`GORK_BASE_URL`, `--base-url`, or a config file. The default path matches Gork
Build: `~/.grok/config.toml`.

```toml
[models]
default = "gork-default"

[model.gork-default]
model = "YOUR_RESPONSES_API_MODEL"
base_url = "https://api.x.ai/v1"
backend = "responses"
env_key = ["GORK_API_KEY", "XAI_API_KEY"]
```

Gork-style `[model.<name>]` custom providers and `[mcp_servers.<name>]` tables
are supported. The earlier JSON format remains accepted when passed with
`--config`; an existing `$XDG_CONFIG_HOME/gork-go/config.json` is used as a
fallback when `~/.grok/config.toml` does not exist.

Model entries accept `context_window` and
`auto_compact_threshold_percent`. The default threshold matches Gork Build at
85%; `GROK_AUTO_COMPACT_THRESHOLD_PERCENT` has highest precedence. When the
reported input-token usage reaches the threshold, Gork Go asks the current
model for a successor handoff summary, starts a fresh response chain, and logs
the compaction. Chat Completions and Anthropic histories are reset only after a
summary succeeds. `[compaction.pruning]` supports the compatible old-tool-result
soft/hard pruning fields shown in `config.example.toml`.

The default model transport is the Responses API. For OpenAI-compatible
providers that only expose Chat Completions, use `--backend chat_completions`
or set `GORK_BACKEND=chat_completions`. The adapter preserves local multi-turn
message history, streams text, reassembles incremental function-call arguments,
and feeds tool results back as `tool` messages. Cross-process `--resume` needs
server-side response IDs and is therefore limited to the Responses backend.

Anthropic-compatible Messages endpoints are available with
`--backend anthropic_messages`. That adapter uses the `x-api-key` and
`anthropic-version` headers, streams text and `input_json_delta` events, and
maintains the required assistant `tool_use` / user `tool_result` block ordering.

API keys may be put in the config for compatibility, but environment variables
are preferred so secrets are not stored in a plain-text file.

## Run

```sh
./gork --workspace /path/to/project "inspect this repository and run its tests"
```

Prompts can also be piped through stdin:

```sh
printf '%s\n' 'explain the failing test' | ./gork --workspace .
```

Use `--interactive` for a persistent multi-turn terminal session. The response
ID from each turn is linked into the next turn without resending the entire
conversation:

```sh
./gork --interactive --workspace .
```

Use `--tui` for the full-screen interface:

```sh
./gork --tui --workspace .
```

The TUI streams model output as it arrives, keeps a scrollable transcript,
supports Unicode input, displays tool status, cancels the current turn with
Ctrl-C, and presents write/Shell/MCP approval prompts inside the alternate
screen. Page Up/Page Down scroll, Ctrl-Q exits, and an optional prompt argument
starts the first turn immediately. When providers report usage, the status line
shows input tokens, context window and percentage.

Use `--goal` for bounded autonomous continuation. The runtime keeps starting
new turns until the model calls `update_goal` with `completed=true` or a genuine
`blocked_reason`; `--goal-runs` controls the safety cap (default 10):

```sh
./gork --goal --goal-runs 8 --workspace . "implement and verify the feature"
```

Progress-only `update_goal` calls keep the goal active. Goal mode is explicit
and cannot be combined with the interactive REPL or TUI.

Use `--acp` to run an Agent Client Protocol v1 agent over JSON-RPC stdio for
editor/IDE integrations:

```sh
./gork --acp
```

Each `session/new` gets its own workspace, tool state, model history, local
session log, MCP/LSP processes, and cleanup lifecycle. The baseline
`initialize`, `session/new`, `session/list`, `session/load`, `session/resume`,
`session/prompt`, `session/update`, `session/cancel`, and `session/close`
methods are supported. Persisted sessions use stable, path-safe IDs; load
replays completed user/agent text history while resume reconnects without
replay. Text streams as
`agent_message_chunk`, while tools emit correlated `tool_call` and
`tool_call_update` lifecycle events. Stdio MCP servers supplied by the client
in `session/new` are merged with configured servers for that session. Default
prompt approvals use ACP's bidirectional `session/request_permission`, linked
to the actual tool call, so protocol stdin is never consumed by a CLI prompt.
`--approval auto` and `--approval deny` remain available for clients that
intentionally want a fixed policy.

Git repositories also expose compatible hunk tracker ACP extensions:
`x.ai/hunk-tracker/get-hunks`, `get-files`, `get-summary`, `hunk-action`,
`file-action`, and `all-action`.
Tracked, staged, and text untracked changes are included; mutations performed
through Gork file tools are attributed to the agent, while other files are
reported as external. Actions accept `accept` or `reject`: accepted hunks are
hidden for the current session, while rejection restores the recorded old text
only when the current line range still exactly matches the hunk. A stale hunk
fails closed instead of overwriting newer edits.

The ACP server also supports `x.ai/git/worktree/create`, `list`, `show`,
`apply`, and `remove`. Creation accepts the compatible `linked`, `standalone`, and `git`
types plus `clean` or `dirty` copy modes. Dirty creation preserves staged,
unstaged, and text/binary untracked files; an explicit `gitRef` always creates
a clean checkout. Managed worktrees are persisted in `worktrees.json` beside
the session state and fresh creation emits `x.ai/git/worktree/status`. Removal
accepts only registered worktree IDs or paths, supports `dryRun`, and requires
`force` when Git refuses to remove a dirty linked worktree.
`apply` supports `overwrite` and conflict-aware `merge`: merge writes a file
only while the main checkout still matches its current HEAD version, and
returns compatible `base`/`ours`/`theirs` conflict records otherwise.

Local mutations require confirmation by default:

- `--approval prompt`: ask before every file mutation and shell command.
- `--approval deny`: allow only read-only tools.
- `--approval auto`: approve all available local tools. Use only in a trusted
  workspace and environment.

Repeatable `--allow 'Tool(pattern)'` and `--deny 'Tool(pattern)'` rules refine
the base mode. Deny always wins, allow bypasses the base prompt, and unmatched
actions fall back to `--approval`. Bare patterns target Bash-compatible shell
actions:

```sh
./gork --allow 'Bash(git *)' --deny 'Bash(git push --force*)' --workspace .
```

Persistent rules use Gork Build's `[permission]` schema. Rule precedence is
deny, then ask, then allow, regardless of declaration order:

```toml
[[permission.rules]]
action = "allow"
tool = "bash"
pattern = "git *"

[[permission.rules]]
action = "ask"
tool = "bash"
pattern = "git push *"
```

The `web_fetch` tool retrieves bounded public HTTP(S) text after approval.
Loopback, private, link-local, multicast and unspecified addresses are rejected
both during URL validation and again when dialing (including redirects). Use a
WebFetch permission with `pattern_mode = "domain"` for host-based rules.

The Gork Build-compatible file surface includes `read_file`, `list_dir`,
`grep`, and `search_replace`; text reads support positive or negative line
offsets and use the original `LINE_NUMBER→LINE_CONTENT` format. The earlier
`list_files`, `search_files`, `write_file`, `edit_file`, and `shell` tools remain
available. `todo_write` maintains the ordered task list across tool calls with
replace, merge, partial-status-update, and duplicate-ID behavior matching the
reference runtime. `update_goal` reports progress and terminal state when
`--goal` is active and rejects calls outside goal mode. The compatible command
surface is `run_terminal_cmd`,
`get_task_output`, and `kill_task`, including
foreground exit-status output, background task IDs, multi-task polling/waiting,
process-group termination, and persistent cwd/environment/function/alias state
between foreground calls. Background commands inherit a state snapshot without
changing the foreground session when they finish. The earlier aliases
`start_background_command`, `get_background_command_output`, and
`kill_background_command` remain available. Output is captured in a bounded
tail buffer, process groups are terminated on request, and every remaining
process is cleaned up when Gork exits. File operations resolve symlinks and
reject paths outside the selected workspace. Shell commands start in the
workspace, but they are not yet kernel-sandboxed; approval remains a security
boundary.

Each run is recorded as a mode-0600 JSONL event log under the user cache
directory. `--session-dir` selects another location.

Resume the most recent completed turn, or a specific session log, with:

```sh
./gork --interactive --resume latest --workspace .
./gork --resume /path/to/session.jsonl "continue the implementation"
```

Resume refuses symlinks, oversized logs, malformed events, and sessions whose
only model response still has pending tool calls. This prevents a new prompt
from being attached to a half-finished tool transaction.

## MCP servers

Stdio MCP servers can be configured in the same JSON file. Gork Go performs the
MCP initialization handshake, discovers all paginated tools, exposes them to
the model as `mcp__SERVER__TOOL`, and forwards tool results back into the agent
loop. MCP tool calls use the same approval mode as local mutations because tool
annotations are only hints and cannot be treated as a security boundary.

```toml
[mcp_servers.filesystem]
command = "npx"
args = ["-y", "@modelcontextprotocol/server-filesystem", "/project"]
```

Server processes inherit the current environment, with optional per-server
`env` overrides. They start in the selected workspace and are shut down when
the agent exits.

MCP Streamable HTTP endpoints use `url` instead of `command`. Gork sends the
negotiated protocol and session headers, accepts both JSON and SSE responses,
and closes stateful sessions with DELETE:

```toml
[mcp_servers.remote]
url = "https://mcp.example.com/rpc"
headers = { Authorization = "Bearer token" }
```

## Language servers

Language Server Protocol processes can also be configured per workspace. When
at least one is enabled, Gork exposes a single `lsp` tool for hover text,
definitions, references, document/workspace symbols, and published diagnostics.
Paths are still confined to the selected workspace.

```toml
[lsp_servers.gopls]
command = "gopls"
extensions = [".go"]

[lsp_servers.typescript]
command = "typescript-language-server"
args = ["--stdio"]
extensions = [".ts", ".tsx", ".js", ".jsx"]
```

Servers use LSP's framed stdio JSON-RPC transport, receive the workspace root
during initialization, and are shut down on exit. Extension filters are
optional; entries may be written with or without the leading dot.

## Project instructions and skills

At startup, Gork Go discovers project instruction files compatible with Gork
Build (`AGENTS.md`, `Agents.md`, `AGENT.md`, `Claude.md`, `CLAUDE.md`,
`.claude/CLAUDE.md`, and Markdown files under `.gork/rules`, `.claude/rules`,
or `.cursor/rules`). Root instructions are injected with their source paths and
the model is told to check for more deeply nested instructions before editing
files below the root.

Reusable skills are discovered from `.gork/skills`, `.agents/skills`, and
`.claude/skills` in both the workspace and user home. Only skill metadata is
included initially. The model calls the `skill` tool to load the full
`SKILL.md` when a task matches it; workspace skills override same-named user
skills.

## Privacy

Gork Go does not include product analytics, research trace uploads, repository
packaging uploads, or vendor auto-update code. Prompts and tool results used by
the agent are sent to the configured model endpoint because remote inference
requires them. Session records stay local unless the user moves or uploads
them.

## License and attribution

Apache License 2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
