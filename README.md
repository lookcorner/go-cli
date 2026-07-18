# Gork Go

The implementation follows the lightweight DDD boundaries documented in
[`ARCHITECTURE.md`](ARCHITECTURE.md).

Gork Go is a Go reimplementation of [Gork Build](https://github.com/thedavidweng/gork-build),
the privacy-oriented community build of the Grok Build coding agent.

The project is under active compatibility development. The current runtime is
a usable headless coding agent with a Responses-compatible streaming client,
function-tool loop, workspace confinement, explicit mutation approval and
local JSONL session records. See [COMPATIBILITY.md](COMPATIBILITY.md) for the
feature-by-feature status.

Interactive REPL and full-screen sessions accept `/compact` to summarize the
current completed response chain and continue from a fresh context.

Workspace instruction and skill discovery respects repository and global Git
ignore rules through Git's own matching engine. Project instructions load from
the Git root through the current workspace so deeper files take precedence.

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

For the default xAI endpoint, `gork login` performs an OIDC browser login with
PKCE and a loopback callback. Use `--device-auth` for the OAuth device flow.
Both store scoped credentials in `~/.grok/auth.json` (or `$GROK_HOME/auth.json`).
Gork Go loads that token when no API-key environment variable is set and
refreshes it within five minutes of expiration. Issuer, client ID, scopes and
audience honor `GROK_OAUTH2_*` / `GROK_OIDC_*` environment overrides. Login
opens the verification URL with the platform browser; use `--no-browser` in SSH
or headless environments and paste the callback URL when prompted. Refresh and
write transactions coordinate through
`auth.json.lock`, including cancellation and stale-lock recovery. Model API
requests retry once after a 401 using a freshly resolved credential; concurrent
401s reuse the first successful refresh.

For managed credential brokers, set `GROK_AUTH_PROVIDER_COMMAND` or
`auth_provider_command` under `[grok_com_config]` in `config.toml`. The command writes either a bare token
or `{"access_token":"...","refresh_token":"...","expires_in":3600}` to stdout;
stderr remains visible for login instructions. `GROK_AUTH_TOKEN_TTL` (or
`auth_token_ttl`) gives bare tokens a proactive refresh lifetime. Refresh calls
receive `GROK_AUTH_EXPIRED=1` and are limited to five seconds.
Use `gork logout` to remove only the current issuer/client credential scope.

`force_login_team_uuid` under `[grok_com_config]` accepts one team UUID or an array of
allowed UUIDs. It rejects personal/wrong-team tokens before persistence and
disables API-key authentication so the policy cannot be bypassed. OAuth2
`principal_type` and `principal_id` only preselect the consent-screen identity;
they do not enforce membership by themselves. An empty allowed-team array
blocks every login.

`preferred_method` under `[auth]` may be `oidc` or `api_key`. Automatic credential
selection fails closed when the selected method is unavailable instead of
silently falling back to the other method.

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

On Unix, `/etc/grok/managed_config.toml` is the lowest disk layer. It is
overlaid by `$GROK_HOME/managed_config.toml` and then the user `config.toml`.
Nested tables merge recursively, arrays replace, and environment/CLI values are
applied afterward.

Managed authentication and permission policy may be placed in
`$GROK_HOME/requirements.toml` (or `~/.grok/requirements.toml`). On Unix,
`/etc/grok/requirements.toml` is applied afterward and wins conflicts. These
layers override user config and relevant environment settings; deny rules still
win over CLI `--allow`. Invalid requirements soft-fail by default. Set
`fail_closed = true` in a valid layer, or
`GROK_MANAGED_CONFIG_FAIL_CLOSED=true`, to make policy loading errors stop
startup. See `requirements.example.toml`.

`[models].web_search` may select another `[model.<name>]` Responses provider
for the `web_search` tool; otherwise a Responses-backed main model is reused.
`GORK_WEB_SEARCH_API_KEY`, `GORK_WEB_SEARCH_BASE_URL`, and
`GORK_WEB_SEARCH_MODEL` provide environment overrides. Model entries accept `context_window` and
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

LSP server entries accept `initialization_options` and `settings`. Settings are
sent after initialization and returned to server-initiated
`workspace/configuration` requests by section.

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
replays completed user/agent text and image history while resume reconnects without
replay. Prompts accept embedded text/resources plus validated base64 or remote
HTTP(S) images; audio is not yet supported. Text streams as
`agent_message_chunk`, while tools emit correlated `tool_call` and
`tool_call_update` lifecycle events; image and rendered PDF results are included
as native ACP image content. Stdio, Streamable HTTP, and standalone SSE
MCP servers supplied by the client in `session/new`, `session/load`, or
`session/resume` are validated and merged with configured servers for that
session. Stdio and standalone SSE servers may request permission-gated MCP
sampling through the configured model without modifying the main conversation
history. Default
prompt approvals use ACP's bidirectional `session/request_permission`, linked
to the actual tool call, so protocol stdin is never consumed by a CLI prompt.
`--approval auto` and `--approval deny` remain available for clients that
intentionally want a fixed policy.

`x.ai/session/fork` creates a persisted child session without starting it. It
supports client-provided IDs, model overrides, a new working directory, and
inclusive `targetPromptIndex` truncation; later load/resume uses the stored
model override.

ACP sessions expose `x.ai/rewind/points` and `x.ai/rewind/execute` for
`all`, `conversation_only`, and `files_only` rewind (`code_only` remains an
alias). Rewinds append a timeline marker instead of deleting history, restore
the selected Responses continuation ID, and rebuild visible Chat Completions or
Anthropic history. Before/after snapshots for `write_file`, `edit_file`, and
`search_replace` are persisted with each prompt. Preview reports external file
conflicts; `force: true` restores the earliest content and removes files created
after the target checkpoint. Shell-created file changes are not checkpointed.

Unix ACP clients may also create interactive terminals through
`x.ai/terminal/pty/create`, stream base64 input and output, resize or reconnect
with bounded output replay, list terminals, and terminate them. Foreground
commands emit transition-only `process_started` and `process_ended`
notifications. PTYs are owned by the ACP server connection and are cleaned up
when it closes.

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
`apply`, `remove`, `create_from_worktree`, and
`create_from_worktree_sync`. Creation accepts the compatible `linked`, `standalone`, and `git`
types plus `clean` or `dirty` copy modes. Dirty creation preserves staged,
unstaged, and text/binary untracked files; an explicit `gitRef` always creates
a clean checkout. Managed worktrees are persisted in `worktrees.json` beside
the session state and fresh creation emits `x.ai/git/worktree/status`. Removal
accepts only registered worktree IDs or paths, supports `dryRun`, and requires
`force` when Git refuses to remove a dirty linked worktree.
`apply` supports `overwrite` and conflict-aware `merge`: merge writes a file
only while the main checkout still matches its current HEAD version, and
returns compatible `base`/`ours`/`theirs` conflict records otherwise.
Fork creation preserves dirty state and resolves linked descendants back to
the true main repository rather than nesting repository identity.

Worktree management extensions `x.ai/git/worktree/gc`, `db/stats`,
`db/rebuild`, and `db/path` are supported. GC accepts compatible duration
strings such as `7d`, `24h`, `30m`, and `60s`; `dryRun` reports candidates
without changing disk or registry state, while non-forced collection protects
worktrees whose creator process is still alive. Database rebuild discovers
linked and standalone worktrees under the managed worktree directory.

Local session-aware worktree flows are available through
`x.ai/session/resolve_local_for_worktree_resume`,
`x.ai/git/worktree/resume_session`, and `x.ai/session/rehydrate`. Resume copies
the validated JSONL event stream to a new session ID, rebinds its CWD to the
new worktree (including a source subdirectory offset), and preserves loadable
conversation history. Rehydrate recreates a missing linked worktree while
keeping an existing local session ID. Remote registry/archive recovery is not
yet available and returns an explicit error.

New sessions record their Git HEAD. With `restoreCode: true`, worktree resume
checks out that historical commit and reports `restoreDegree: "head_only"`.
Dirty staged, unstaged, and untracked state in the new worktree is saved to a
labeled stash before checkout; full archive restoration remains unavailable.

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

The `web_search` tool uses the configured Responses model's native web-search
capability and accepts optional domain filters. The `web_fetch` tool retrieves
bounded public HTTP(S) text after approval, converts HTML to Markdown, resolves
relative links, removes executable/embed content, and rejects binary responses.
HTTP URLs are upgraded to HTTPS; redirects are limited and may not cross hosts.
Successful text responses are cached in memory for 15 minutes, up to 128 pages.
Converted pages larger than 3% of the configured model context are previewed
inline and persisted under the current session's `web_fetch` artifacts. The
returned path can be inspected with `read_file` (or `bash` for long-line data);
path-bearing overflow responses are fetched and materialized again instead of
being reused across sessions from cache.
Fetched PDFs, PNG/JPEG/GIF/WebP images, and videos are likewise saved under the
current session's `downloads`, `images`, or `videos` artifact directory. Known
image/video formats are checked against their magic bytes, SVG is rejected,
and downloaded PDFs/images can be reopened through `read_file`.
Loopback, private, link-local, multicast and unspecified addresses are rejected
both during URL validation and again when dialing (including redirects). Use a
WebFetch permission with `pattern_mode = "domain"` for host-based rules.
`[toolset.web_fetch]` may set `proxy_endpoint` and an `allowed_domains` list;
entries may be host-wide (`docs.example.com`) or path-scoped
(`example.com/docs`). Omitting the list uses Gork Build's built-in documentation
domain allowlist. TOML proxy configuration takes precedence over
`GROK_WEB_FETCH_PROXY`; a configured list replaces the defaults, and an
explicit empty list blocks all fetches.

The Gork Build-compatible file surface includes `read_file`, `list_dir`,
`grep`, and `search_replace`; text, extracted PPTX, and PDF `format: "text"`
reads support positive or negative line offsets and use the original
`LINE_NUMBER→LINE_CONTENT` format. PDF text reads accept `pages` ranges and
require an explicit range when the document exceeds ten pages. PDF image mode
renders selected pages at 150 DPI through Poppler's `pdftoppm`. PNG, JPEG, GIF,
and WebP reads are validated and forwarded as native image content to the
Responses, Chat Completions, or Anthropic backend. The earlier
`list_files`, `search_files`, `write_file`, `edit_file`, and `shell` tools remain
available. `list_dir` summarizes subdirectories that exceed its output budget
with file counts and their most common extensions. `search_replace` preserves
CRLF files and safely matches common
rich-text typography such as smart quotes, em dashes, ellipses, and
non-breaking spaces. `todo_write` maintains the ordered task list across tool calls with
replace, merge, partial-status-update, and duplicate-ID behavior matching the
reference runtime. `update_goal` reports progress and terminal state when
`--goal` is active and rejects calls outside goal mode. The compatible command
surface is `run_terminal_cmd`,
`get_task_output`, and `kill_task`, including
foreground exit-status output, background task IDs, multi-task polling/waiting,
process-group termination, and persistent cwd/environment/function/alias state
and Bash/Zsh options between foreground calls. `GROK_SHELL` overrides `SHELL`
when it points to Bash or Zsh; otherwise Bash is the fallback. `nounset` is not
replayed, and Zsh unmatched globs remain literal, matching the reference safety
behavior. Background commands inherit a state snapshot without changing the
foreground session when they finish. The earlier aliases
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
the agent exits. Text, structured, and validated image tool results are
forwarded to the model without flattening images into JSON text.

MCP Streamable HTTP endpoints use `url` instead of `command`. Gork sends the
negotiated protocol and session headers, accepts both JSON and SSE responses,
and closes stateful sessions with DELETE:

```toml
[mcp_servers.remote]
url = "https://mcp.example.com/rpc"
headers = { Authorization = "Bearer token" }
```

Legacy standalone SSE servers use the same URL form with `type = "sse"` (a
URL ending in `/sse` is also detected automatically). Gork keeps the GET event
stream open and sends JSON-RPC requests to the same-origin endpoint announced
by the server:

```toml
[mcp_servers.legacy]
url = "https://mcp.example/sse"
type = "sse"
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
during initialization, and are shut down on exit. Server-requested text edits
are applied with workspace confinement, UTF-16 range and document-version
checks, and atomic file replacement. LSP create/rename/delete resource
operations are rejected. Extension filters are optional; entries may be
written with or without the leading dot.

## Project instructions and skills

At startup, Gork Go discovers project instruction files compatible with Gork
Build (`AGENTS.md`, `Agents.md`, `AGENT.md`, `Claude.md`, `CLAUDE.md`,
`.claude/CLAUDE.md`, and Markdown files under `.gork/rules`, `.claude/rules`,
or `.cursor/rules`). Root instructions are injected with their source paths and
the model is told to check for more deeply nested instructions before editing
files below the root.

Reusable skills are discovered from `.grok/skills`, `.gork/skills`,
`.agents/skills`, `.claude/skills`, and `.cursor/skills`. Flat Markdown files
under the matching `commands` directories are loaded through the same Skill
tool, with the filename used as the fallback name; a same-named `SKILL.md` has
priority over a command. Project directories are scanned from the Git root
through the workspace, and user skills honor `GROK_HOME`. Only skill metadata
is included initially. The model calls the `skill` tool to load the full file
when a task matches it; deeper project skills override same-named user or parent
skills.
Known Cursor and Claude built-in Skill copies found inside their vendor
directories are ignored; user-authored skills with the same names elsewhere
remain available.
When copied directories in the same scope still declare the same frontmatter
name, the directory-name owner keeps the bare name and other copies remain
available under their normalized directory names. Cross-scope collisions still
use the normal workspace-over-user priority.
Skills with a scalar or list `paths` value in YAML frontmatter stay hidden until a successful
file read, directory listing, or edit touches a matching path.
`user-invocable: false` keeps a Skill in the model-facing catalog but removes it
from the Skill tool; only the literal YAML value `true` enables invocation.
Known user-invocable Skills can also be referenced directly in prompts as
`/skill-name arguments`. Multiple references are expanded into the reference
`<skill_information>` envelope before the model call. Skill bodies support
`$ARGUMENTS`, `$ARGUMENTS[N]`, `$N`, `${SKILL_DIR}`, and
`${CLAUDE_SKILL_DIR}`, `${SESSION_ID}`, and `${CLAUDE_SESSION_ID}`
substitutions; arguments are appended when no argument placeholder is present.

The `[skills]` config accepts additional directories or individual `SKILL.md`
files. Paths support `~`; relative paths resolve from the workspace. `ignore`
removes matching path prefixes, while `disabled` keeps named skills discoverable
but hides them from model invocation:

```toml
[skills]
paths = ["~/shared-skills", "project-skills"]
ignore = ["~/shared-skills/experimental"]
disabled = ["manual-only"]
```

Claude and Cursor discovery surfaces default to enabled. They can be disabled
independently in `config.toml`, with matching `GROK_<VENDOR>_<SURFACE>_ENABLED`
environment variables taking precedence:

```toml
[compat.cursor]
skills = false
rules = false
agents = false
```

## Privacy

Gork Go does not include product analytics, research trace uploads, repository
packaging uploads, or vendor auto-update code. Prompts and tool results used by
the agent are sent to the configured model endpoint because remote inference
requires them. Session records stay local unless the user moves or uploads
them.

## License and attribution

Apache License 2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
