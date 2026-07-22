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

Interactive REPL, full-screen, and ACP sessions accept `/compact` to summarize
the current completed response chain and continue from a fresh context. When
workspace memory is enabled, `/flush` saves reusable context without changing
that response chain, while `/memory` lists the safe global, workspace, and
session Markdown sources without invoking the model. They also accept
`/loop [interval] <prompt>`, which expands to the reference `scheduler_create`
workflow without inventing a default interval. ACP advertises the enabled
commands and user-invocable workspace skills through `x.ai/commands/list`.
Skill collisions use scope-qualified names with source metadata, argument
prompts use ACP `input.hint`, and initialization advertises commands that are
safe before a session exists. ACP also emits `memory_files` metadata updates,
and exposes `x.ai/compact_conversation` with optional user context,
`x.ai/memory/flush`, plus the bounded, history-isolated `x.ai/memory/rewrite`
note formatter. Extension-triggered compaction returns an empty result and does
not emit a prompt-completion event. Successful manual compaction first publishes
a persisted `x.ai/session_notification` with `auto_compact_completed` so live
and resumed clients reset context state consistently.

ACP clients can request turn-end ghost text through `x.ai/suggestPrompt`. The
extension echoes the client generation, returns `null` when no safe suggestion
is available, and samples a bounded text-only transcript without tools or parent
history mutation. `GROK_PROMPT_SUGGESTIONS_MODEL` overrides the request model
hint; otherwise the dedicated `grok-build-0.1` suggestion model is used.

ACP clients can enable workspace file notifications with
`clientCapabilities._meta["x.ai/fs_notify"]`. Boolean `true` streams absolute
create, modify, and remove events. Object form can request a Gitignore-aware
initial file index plus add/remove deltas with `index: true`, and configure
`debounce_ms` and extra `ignore` globs. File watchers stop when their session
closes.

Clients can separately opt into `x.ai/git_head_changed` with the boolean
`clientCapabilities._meta["x.ai/gitHeadChanged"]`. Each session publishes its
initial branch and linked-worktree identity, then sends deduplicated updates
after successful edit/shell tools or an external Git HEAD change. Detached
HEADs use `null` for `branch`, normal repositories use `null` for `mainRepo`,
and the watcher is released with the session.

ACP `x.ai/suggest` provides interactive shell completion from workspace prompt
history, `$PATH` executables, and filesystem entries. It returns safe whole-line
insertions plus atomic token ranges for newer clients, respects shell quoting,
and supports deterministic token-only completion. Clients may also request a
best-effort AI completion; strong history matches skip sampling, and failures or
the two-second timeout leave the local results intact.

The fire-and-forget `x.ai/yolo_mode_changed` notification switches every live
session on the ACP connection among ask, always-approve, and classifier-based
auto behavior. Explicit `auto_mode` wins over the compatible `permission_mode`
hint, while an explicitly enabled yolo mode wins over auto. Explicit deny mode
and deny/ask/allow permission rules remain authoritative. Managed
always-approve locks and the `auto_mode.enabled` gate remain enforced during
live switches. ACP `session/new`, `session/load`, and `session/resume` also honor
boolean `_meta.yoloMode` and `_meta.autoMode` (or `_meta.auto_mode`) overrides;
invalid metadata falls back to startup defaults, and the same managed gates
remain authoritative. `session/new` accepts a validated UUID in `_meta.sessionId`
and a configured profile or underlying model in `_meta.modelId` (unknown models
fall back to the default); `session/load` accepts `_meta.noReplay=true` when the
client already owns the transcript and only needs the persisted runtime state.

ACP permission prompts also offer an exact-request `allow_always` choice for
the current session. `x.ai/permissions/reset` clears those remembered grants
from every live session without changing its ask, auto, always-approve, deny,
or managed-policy mode.
New and loaded sessions return the active and available model state. An idle
session accepts `session/set_model`, persists the selection, rebuilds backend
history from the completed transcript, updates future subagent and goal-role
defaults, and broadcasts `model_changed` before replying. The broadcast is live
only and intentionally has no replay cursor. Busy sessions reject
the switch so an in-flight turn cannot cross model backends. When a persisted
model is no longer visible or allowed, loading the session automatically selects
a visible model from the same `grok-build` or non-`grok-build` family, starts a
fresh response chain from the saved transcript, and broadcasts the live-only
`model_auto_switched` update. A cross-family fallback, or an allowlist that
excludes every known model, blocks prompts until the client selects an allowed
model with `session/set_model`.

Completed ACP prompts publish `x.ai/session/prompt_complete` before their RPC
response. Prompt responses include `_meta` correlation for the session,
request/prompt, model, token usage, optional turn, and cancellation trigger;
queued prompts removed before execution return cancelled without publishing a
false completion event.

The model can call `enter_plan_mode` to enter a persisted, read-only planning
phase. While active, workspace mutations are limited to `.grok/plan.md`; shell,
background, scheduler mutation, image-generation, task, monitor, and MCP tools
are blocked. `exit_plan_mode` presents that file for approval, including the
ACP `x.ai/exit_plan_mode` reverse request, and applies the decision before the
next model step in the same turn. Clients may also send the fire-and-forget
`x.ai/toggle_plan_mode` notification; it persists the new mode and publishes the
same `current_mode_update` as `session/set_mode`.

`ask_user_question` accepts one or more option-based questions and remains
available in plan mode. ACP sessions send `x.ai/ask_user_question`, advertise
the pending interaction, wait for accepted, cancelled, clarification, or
skip-interview responses, and feed the formatted answer back into the same
tool loop. One questionnaire has a shared 30-minute timeout, overridable with
`GROK_ASK_USER_QUESTION_TIMEOUT_SECS`. The TUI renders an in-place option
selector, while REPL, one-shot, and goal-mode terminal runs accept option
numbers, comma-separated multi-select values, or free text for Other. The REPL
uses a question-aware input arbiter so pending prompts, scheduled turns,
permission confirmations, and tool questions cannot read the same line.
`[toolset.ask_user_question]` accepts `timeout_enabled` and positive
`timeout_secs` values. Environment seconds override normal config, while
requirements policy has final precedence. Invalid environment values are
ignored, and an explicitly configured zero uses the 30-minute default rather
than disabling the timer.

The optional anchor-validated file toolset is enabled with:

```toml
[toolset]
file_toolset = "hashline"

[toolset.hashline]
scheme = "chunk" # or "content_only"
hash_len = 3
chunk_size = 8
```

It replaces the standard read/edit/search entry points with `hashline_read`,
`hashline_edit`, and `hashline_grep`. Edits use anchors copied from read or grep
output; a stale or overlapping operation rejects the entire batch before disk
mutation. Successful edits use the existing permission, rewind-checkpoint, and
atomic-write paths and return fresh anchors.

Workspace instruction and skill discovery respects repository and global Git
ignore rules through Git's own matching engine. Project instructions load from
the Git root through the current workspace so deeper files take precedence.

## Build

Go 1.25 or newer is required.

```sh
go test ./...
go build -o gork ./cmd/gork
# Release build with version-aware config matching:
make build VERSION=0.2.0
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

Enterprise managed policy can be fetched with `gork setup`. Configure
`GROK_DEPLOYMENT_KEY`, or sign in with a team account, then the client requests
`{cli_chat_proxy_base_url}/deployment/config`. Override the endpoint with
`GROK_MANAGED_CONFIG_URL` or `[endpoints].managed_config_url`. Served
`managed_config.toml` and `requirements.toml` files are replaced atomically;
withdrawn files are removed. Team login performs the same sync on a best-effort
basis.
`gork setup --json` returns the served documents without changing disk. Setup
retries transient transport and 5xx failures with bounded exponential backoff;
session startup performs an eight-second repair only when the cache marker is
missing, belongs to another principal, or references a missing/invalid artifact.

The signed-policy implementation verifies the server's exact Ed25519 payload,
principal binding, expiry and byte-for-byte disk contents before enforcing a
fail-closed policy. The trusted key set is compile-time only and intentionally
empty in this compatibility build, matching the referenced Gork Build commit;
there is no environment switch that can enable or disable signature trust.

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
allowed_models = ["gork-*"]

[model.gork-default]
model = "YOUR_RESPONSES_API_MODEL"
name = "Gork Default"
base_url = "https://api.x.ai/v1"
backend = "responses"
env_key = ["GORK_API_KEY", "XAI_API_KEY"]
context_window = 131072
supports_reasoning_effort = true
reasoning_effort = "high"
reasoning_efforts = ["low", "medium", "high", { id = "max", value = "xhigh", label = "Max" }]

[auto_mode]
enabled = true
prompt_type = "full"
classifier_model = "gork-default"
reasoning_effort = "low"
```

`models.allowed_models`, `hidden_models`, and `disabled_models` accept glob
patterns matched against both catalog keys and provider model IDs. Models that
do not match the allowlist remain in the internal catalog for availability
checks but are omitted from user switching; hidden models are omitted from ACP
model pickers while remaining explicitly selectable by catalog ID. Disabled
models cannot be resolved. If the configured default is filtered out, new ACP
sessions use the first visible, selectable catalog entry instead when one
exists. Individual `[model.<name>]` entries may also set `hidden = true`.

While the ACP server is running, model changes in `config.toml`, local/system
`managed_config.toml`, and `requirements.toml` are detected automatically.
The `x.ai/internal/reload_models` endpoint triggers the same disk reload for
every live session and returns the resolved catalog size.
Fresh reference-compatible `models_cache.json` catalogs are loaded from
`$GROK_HOME` or `~/.grok` and can be refreshed through
`x.ai/internal/reload_models_cache`. Version, five-minute TTL, authentication
method and models-list origin must all match before cached endpoints are used.
Connected clients receive `x.ai/models/update`; idle sessions switch when an
explicit default changes or their current model disappears, while busy sessions
defer that switch until the next prompt. Future subagents use the refreshed
catalog. A removed local filter is cleared, but an externally supplied filter
that is not owned by local config remains fail-closed.

Gork-style `[model.<name>]` custom providers and `[mcp_servers.<name>]` tables
are supported. The earlier JSON format remains accepted when passed with
`--config`; an existing `$XDG_CONFIG_HOME/gork-go/config.json` is used as a
fallback when `~/.grok/config.toml` does not exist.

On Unix, `/etc/grok/managed_config.toml` is the lowest disk layer. It is
overlaid by `$GROK_HOME/managed_config.toml` and then the user `config.toml`.
Nested tables merge recursively, arrays replace, and environment/CLI values are
applied afterward.

Every TOML disk layer may include `[[version_overrides]]` with inclusive
`minimum_version` and/or `maximum_version`. Matching patches are applied in
ascending minimum-version order before that layer is merged; equal minimums
retain declaration order so the later patch wins.

Managed authentication and permission policy may be placed in
`$GROK_HOME/requirements.toml` (or `~/.grok/requirements.toml`). On Unix,
`/etc/grok/requirements.toml` is applied afterward and wins conflicts. These
layers override user config and relevant environment settings; deny rules still
win over CLI `--allow`. Invalid requirements soft-fail by default. Set
`fail_closed = true` in a valid layer, or
`GROK_MANAGED_CONFIG_FAIL_CLOSED=true`, to make policy loading errors stop
startup. See `requirements.example.toml`.

Administrators may set `[ui] disable_bypass_permissions_mode = true` in any
`requirements.toml` layer to disable always-approve for CLI, TUI, ACP, and
subagents. The legacy `[ui] yolo = false` form has the same effect. Once enabled
by a requirements layer, a later layer cannot remove the lock; non-boolean
values are ignored. These keys in ordinary `config.toml` files are not managed
policy. Explicit deny modes and deny rules remain authoritative.

`[auto_mode]` controls classifier-based automatic permission decisions. It is
enabled by default; `GROK_AUTO_PERMISSION_MODE` overrides the local gate, and
requirements may enforce `auto_mode.enabled`. `prompt_type` accepts `full`,
`no_user_tool_prefix`, `bare_instructions`, or `just_command`. An optional
`classifier_model` names a configured `[model.<name>]` profile, while
`reasoning_effort` accepts `none`, `minimal`, `low`, `medium`, `high`, or
`xhigh`. Local fields override remote settings individually, so omitted local
fields can still receive managed remote defaults. The same classifier settings
apply to fresh and resumed subagents.

On macOS, an administrator-forced `requirements_toml_base64` value in the
`ai.x.grok` managed-preferences domain is applied after the system requirements
layer. User-created preference values are ignored; CoreFoundation must report
the value as forced by device management.

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

Sessions may opt into reference-compatible two-pass compaction with
`[features] two_pass_compaction = true` or `GROK_TWO_PASS_COMPACTION=true`.
Ten percentage points before the auto-compact threshold, Gork Go starts a
background prefix summary. At the threshold it merges that note with the
bounded recent text/tool tail; failed, stale, or multimodal prefire state falls
back to the existing single-pass path. Environment configuration overrides the
local feature value, which overrides authenticated remote settings. The default
is off. Chat Completions and Anthropic use isolated history clones for both
summary passes, so speculative prefire never mutates their live conversation.

Cross-session workspace memory is opt-in through `[memory] enabled = true`,
`GROK_MEMORY=true`, or `--experimental-memory`; `--no-memory` is the highest
precedence kill switch. When enabled, `[compaction.memory_flush]` defaults to a
4,000-token headroom and an 8,000-character write limit. Accepted structured
Markdown is written atomically beneath `$GROK_HOME/memory/<workspace>/sessions`,
with exact duplicates skipped. A new session receives a bounded
`<memory-context>` block from workspace/global `MEMORY.md` files and recent
session flushes on its first fresh turn, selected by the first user request.
Short greetings use a project conventions/preferences/architecture fallback
query, and `[memory.initial_injection] min_score` overrides its threshold.
Existing response chains skip re-injection. `/flush` triggers the same quality-gated write path explicitly;
optional `idle_timeout_secs` triggers it after a completed conversation has
remained idle, while session shutdown cancels pending timers. By default,
`[memory.session] save_on_end = true` also records a zero-latency metadata
summary for sessions with at least three real prompts and 50 prompt bytes;
synthetic continuation prompts are excluded. This file-backed path
also exposes `memory_search` and `memory_get` to the model. Search chunks
Markdown by headings and bounded overlap, ranks token matches, and decays old
session notes with a configurable half-life while treating global and workspace
notes as evergreen. `[memory.search.source_weights]` adjusts source priority;
optional `[memory.search.mmr]` promotes diverse results over near-duplicates.
It is a deterministic text-only backend; semantic/vector retrieval remains
pending. Session startup also removes, in the background, empty orphan memory
workspaces older than `[memory.gc].max_age_days` (30 by default); temporary
`tmp*` workspaces use a 7-day limit when they contain sessions and are removed
immediately when empty.
Workspaces beneath system temporary directories are treated as ephemeral:
global memory remains readable and `/remember` can still update it, but session
flushes, session-end summaries, dream output, and workspace memory directories
are not persisted.
During an active REPL, TUI, or ACP session, `/memory off` (or `/mem off`)
removes both retrieval tools and pauses all writes without deleting files;
`/memory on` lazily reopens the same workspace store and restores the tools.
This toggle is session-scoped and does not rewrite `config.toml`.
`gork memory clear` removes the current workspace memory after confirmation;
`--global` selects global memory, `--all` selects both, and `-y` skips the prompt.
`/remember [text]` is available even when retrieval is disabled. It opens a
review before writing: the raw note is always available, a session-isolated
rewrite can be selected when model inference succeeds, and cancel leaves disk
unchanged. Confirmed notes are normalized and appended to the global
`$GROK_HOME/memory/MEMORY.md`, with symlink and size checks.
`/dream` consolidates eligible session logs into the workspace `MEMORY.md`
through an isolated model call. A PID/mtime lock coordinates processes, output
must contain Markdown headings, and logs are deleted only after a successful
write and only when at least five minutes old. `[memory.dream]` defaults to a
session-end check after 4 hours and 3 eligible logs; optional
`check_interval_secs` adds checks while the session is idle.

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

Interactive prompts beginning with `!` run a workspace-scoped Shell command
directly, for example `! git status`; they do not create a model turn.

Use `--tui` for the full-screen interface:

```sh
./gork --tui --workspace .
```

The TUI streams model output as it arrives, renders headings, emphasis, links,
lists, quotes, pipe tables, and inline or fenced code, strips untrusted terminal
control characters, and keeps a scrollable transcript.
On directly supported terminals, Markdown links, bare HTTP(S)/FTP/email URLs, and quoted
absolute file paths use safe OSC 8 targets; spaces in file paths are preserved and
percent-encoded. The same links remain available through tmux 3.4 or newer when
the outer terminal supports OSC 8.
It supports Unicode input, keyboard and content-pane mouse-wheel scrolling,
keeps a scrolled viewport stable while output streams, displays tool status,
cancels the current turn with Ctrl-C, and presents write/Shell/MCP approval
prompts inside the alternate screen. Shift-Tab toggles persisted Plan mode and
shows a visible mode badge. `/always-approve` toggles automatic approval for
otherwise unmatched tool actions while preserving explicit deny and ask rules.
`/plan` idempotently enters Plan mode, while `/plan <description>` enters it and
starts a planning turn. `/quit` and `/exit` close the TUI without a model turn.
`/view-plan`, `/show-plan`, and `/plan-view` open the current confined plan file
in a read-only preview; Esc returns to the conversation.
`/transcript` and `/log` open the completed persisted conversation in the same
read-only viewer, including resumed sessions.
`/rename <title>` and `/title <title>` append a durable title update to the
active session without creating a model turn.
`/export` copies the completed conversation as Markdown; `/export <filename>`
writes it to disk with `~`, spaces, relative paths, and parent creation supported.
While a turn is running, additional prompts are queued FIFO; `/queue` prints a
read-only snapshot and queued prompts run before scheduled wake-ups.
`/tasks` instantly lists background commands, subagents, and scheduled tasks
without creating a model turn.
`/recap` makes a tool-free, display-only model call against the current
conversation. It can run beside an active turn, never enters the prompt queue or
changes conversation history, and discards its result if a newer prompt starts.
`/btw <question>` asks one display-only side question from an isolated snapshot
of the current conversation, including the active prompt. It can run beside the
main turn, exposes tool definitions for context but never executes returned tool
calls, leaves the main history unchanged, and records success or failure in the
session artifact directory.
Left/Right, Home/End, Delete, Backspace, Ctrl-A,
Ctrl-E, Ctrl-U, and Ctrl/Cmd-Z edit or undo the active prompt and structured
response input. Shift/Alt-Enter inserts a newline; Ctrl-M, `/multiline`, or `/ml`
toggles multiline mode, where Enter inserts a newline and Shift/Alt-Enter sends. A trailing
backslash followed by Enter also continues the prompt on the next line. Mouse
clicks can answer approval prompts, select structured question options, and
trigger the three plan-review actions. Double-clicking a question option submits it.
Dragging across visible transcript text copies the selection through OSC 52.
`[ui] keep_text_selection` accepts `flash` (the default), `hold`, or
`word_select`; held highlights clear on Esc or scrolling. `word_select` also
copies a URL or tmux-style character class on double-click and the rendered line
on triple-click. Table drags copy one cell or a rectangular TSV range; triple-clicking
a cell copies that cell, while triple-clicking a table border copies the whole table.
`[ui] word_separators` overrides the default separator set,
including an explicit empty string.
Tab focuses the transcript scrollback; Ctrl-K/Ctrl-J move one line, Ctrl-U/Ctrl-D
move half a page, Page Up/Page Down move a page, and g/G jump to the top/bottom.
Tab or Space returns to the prompt, while typing a letter or `/` returns and forwards it.
Set `[ui] mouse_reporting_toggle = true` (or `GROK_MOUSE_REPORTING_TOGGLE=true`)
to let Ctrl-R release mouse capture while scrollback is focused and press Ctrl-R again
to restore it.
Up/Down browse durable prompt history when the prompt is empty. `/history`
opens fuzzy search over the same workspace-scoped history; Enter or Tab restores
the selected prompt to the composer without submitting it.
`/copy [N]` copies the latest (or Nth-latest) assistant response from the session
transcript to the terminal clipboard.
`/help` lists local commands, `/session-info` shows the active session, workspace,
and model, and `/context` reports the latest available context-window usage.
While scrollback is focused, `/` opens incremental regular-expression search; Enter
accepts the query, Up/Down or `n`/`N` navigates matches, and Esc closes it. `/find <pattern>`
opens the same search directly from the prompt without sending a model turn.
Plan exits open a dedicated full-plan review with approve, revision-feedback,
and abandon outcomes. Ctrl-Q exits, and an optional
prompt argument starts the first turn immediately. When providers report usage,
the status line shows input tokens, context window and percentage.

Use `--goal` for bounded autonomous continuation. The runtime keeps starting
new turns until the model calls `update_goal` with `completed=true` or a genuine
`blocked_reason`; `--goal-runs` controls the safety cap (default 10):

```sh
./gork --goal --goal-runs 8 --workspace . "implement and verify the feature"
```

Add a positive trailing token budget to stop an unfinished goal once parent and
Goal-role model usage reaches the limit:

```sh
./gork --goal --workspace . "implement and verify the feature --budget 500000"
```

Progress-only `update_goal` calls keep the goal active. A `blocked_reason` is
accepted only after three reports in the same process; the first two keep the
Goal active and ask the worker to retry. Completion, Goal creation, and resume
reset this in-memory streak, while message-only progress does not. A completion
claim starts three independent, read-only `general-purpose` skeptics. Two
refutations return the goal to the active loop with their concrete gaps; malformed verdicts
and individual skeptic failures count as refutations, while an unavailable
verifier backend fails open so an internal harness outage cannot strand the
user. The newest rejection gaps are persisted and repeated in every continuation
until a later achieved verdict clears them. Before prompt insertion they are
limited to 4,000 characters and reminder tags plus placeholder braces are
neutralized. A non-cancellation worker infrastructure error immediately pauses
the active Goal with its `Turn failed:` reason, persists it for session reload,
and reports live status as `infra_paused`; resume clears the reason and retries.
User cancellation similarly persists `user_paused` without an error message.
On process reload, an interrupted `active` or `verifying` Goal is also restored
as `user_paused` until an explicit resume, so restart never resumes autonomous
work implicitly.
Verification caps and repeated identical gaps persist distinct
`back_off_paused` and `no_progress_paused` states. Legacy `paused` snapshots
are migrated once from their existing reason and written back explicitly.
Goal mode is explicit and cannot be combined with the interactive REPL or TUI.
The panel defaults to three skeptics. `[goal] verifier_count`, remote
`goal_verifier_count`, and the highest-precedence `GROK_GOAL_VERIFIER_N`
environment variable may select one through five. `[goal] classifier_max_runs`,
remote `goal_classifier_max_runs`, and `GROK_GOAL_CLASSIFIER_MAX` set the
verification-attempt cap (default 10, minimum 1). Repeating the same gaps twice
pauses early instead of continuing a no-progress loop. After a refutation,
`[goal] reverify_after` or `GROK_GOAL_REVERIFY_AFTER` controls when continued
worker rounds explicitly demand another completion claim (default 8, minimum
1); at three times that threshold the reminder escalates. The counter persists
with the Goal and resets whenever verification starts. Goal creation records a
best-effort Git `HEAD` baseline. Each verification writes a bounded cumulative
patch and ordered skeptic-details Markdown under the private session artifact
directory; the prompt links that patch and lists tracked plus untracked paths
so read-only skeptics can corroborate the candidate against live files. If no
Git baseline exists, a bounded modification-time walk synthesizes the patch
while excluding dependency, VCS, cache, and build directories. An existing
`.grok/plan.md` is snapshotted when the goal starts; verifier prompts include
its current path and a bounded baseline-to-current `PLAN_CHANGES` diff.
Goal state is atomically persisted as `goal.json` in the same session artifact
directory. Each Goal receives a persisted UUID v4; live ACP `goal_updated`
notifications reuse it and report parent/verification round totals, cumulative
tokens, completed Goal-role tokens, and the active planner/verifier/strategist
role. They also retain the latest `achieved`/`not_achieved` verdict and its
validated private details path across resume and terminal completion.
Each Goal also owns a private `0700` implementer scratch directory beneath the
session artifact directory. Planner verification paths use the literal
`{SCRATCH}` placeholder; worker prompts resolve it to the private path and
read-only skeptics can inspect saved output there. Unfinished restored Goals
recreate a missing directory, while verified completion and token-budget
termination remove it. Existing files and symbolic links are rejected.
After the first verifier panel runs, its full candidate summary is persisted as
a 4,096-character breadth anchor. Later panels receive that original summary
plus the current round's change note, including after resume, so narrow fixes
cannot hide unverified parts of the original delivery.
`--goal --resume <session.jsonl>` reactivates an unfinished goal,
preserves its original objective and evidence baselines, and resets the
per-resume verifier attempt and stall counters. A supplied prompt is treated as
additional direction; a completed goal requires a new non-empty objective.
Each continuation reads at most 8 KiB of the private plan and inlines the first
unchecked item from `## Task checklist` as the next concrete step. Completed
checkboxes and items under `Non-goals` or `Deviations` are ignored; unsafe frame
tags and overlong model-authored items are neutralized before prompt insertion.
For panels with multiple skeptics, skeptic 0 resumes across verification rounds
to re-check its prior gaps against current files; a failed resume falls back to
a fresh read-only skeptic, while single-skeptic panels always start fresh.
`[goal].skeptic_models` assigns model/profile and agent harness pairs round-robin;
the per-index assignment is frozen in `goal.json` so resumed skeptics keep the
same runtime. `[goal].strategist_model` selects a best-effort diagnostic role
that runs every `[goal].strategist_every` consecutive refutations (default half
the classifier cap), writes a private structural recommendation, and grants
three bounded retry rounds with a relaxed no-progress window. Strategist errors
fail open and revoke that bonus. `GROK_GOAL_STRATEGIST_EVERY` overrides the
cadence, while `GROK_GOAL_USE_CURRENT_MODEL_ONLY=true` immediately clears role
pins and inherited skeptic sessions.

Release builds gate repo-controlled MCP/LSP and enabled project-plugin execution
on folder trust. Interactive CLI startup asks once when executable project
config is present; headless and non-interactive ACP sessions fail closed. ACP
clients may opt into the same decision with
`clientCapabilities._meta["x.ai/folderTrust"].interactive = true`; the agent
sends `x.ai/folder_trust/request` after creating the gated session, and only an
`{"outcome":"trust"}` response grants access and reloads executable project
components. `--trust` records the Git workspace in
`$GROK_HOME/trusted_folders.toml` (normally `~/.grok/trusted_folders.toml`).
Parent trust cascades to child paths while a more specific child decision wins.
Development versions such as `0.1.0-dev` match the reference's unstamped-build
behavior and keep this gate inert.

```sh
./gork --trust --workspace /path/to/project "inspect this repository"
```

Use `--acp` to run an Agent Client Protocol v1 agent over JSON-RPC stdio for
editor/IDE integrations:

```sh
./gork --acp
```

Each `session/new` gets its own workspace, tool state, model history, local
session log, MCP/LSP processes, and cleanup lifecycle. New and restored session
responses include `_meta["x.ai/sessionConfig"]` model/reasoning choices and
`_meta["x.ai/sessionDetail"]` identity, workspace, model, and stored title data.
New-session metadata includes `currentWorkingDirectory`, `isGitRepo`, and
`gitRoot`; restored sessions echo client-owned `x.ai/persist` metadata.
Restored sessions also include `_meta.gitDivergence` when their recorded Git
commit differs from the workspace's current HEAD.
The baseline
`initialize`, `session/new`, `session/list`, `session/load`, `session/resume`,
`session/prompt`, `session/update`, `session/cancel`, and `session/close`
methods are supported. Persisted sessions use stable, path-safe IDs; load
replays completed user/agent text and image history while resume reconnects without
replay. Clients can also fetch rewind-filtered history through
`x.ai/session/updates`, with positive or tail offsets, limits, last-N-turn
selection, prompt boundaries, event cursors, and conversation/tool/lifecycle
ACP envelopes. Large histories can be delivered through ordered
`x.ai/session/updates/chunk` notifications with caller routing metadata.
Prompts accept embedded text/resources plus validated base64 or remote
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
`--approval auto`, `--approval always-approve`, and `--approval deny` remain
available for clients that intentionally want a fixed policy.

For client integration testing, `x.ai/debug/arm_auto_compact` arms one session's
next eligible turn to compact regardless of token usage. The flag is consumed
after that single compaction and accepts either `sessionId` or `session_id`.

When a turn is busy, additional `session/prompt` requests remain pending in a
server-authoritative FIFO queue and run before interjection fallbacks or
scheduled/background wake-ups. Clients receive `x.ai/queue/changed` snapshots
with prompt IDs, positions, versions, ownership and the running prompt ID. The
fire-and-forget `x.ai/queue/remove`, `reorder`, `clear`, `edit`, and `interject`
notifications support owner-aware reconciliation, in-place text edits and
send-now promotion; stale versions are benign no-ops. Cancelling the active turn
continues with the next queued prompt, while closing a session resolves every
queued prompt request as cancelled.

`x.ai/session/fork` creates a persisted child session without starting it. It
supports client-provided IDs, model overrides, a new working directory, and
inclusive `targetPromptIndex` truncation; later load/resume uses the stored
model override.

Persisted and live sessions also expose `x.ai/session/info`, `rename`,
`delete`, and `search`. Rename appends an explicit title event, delete removes
only the validated JSONL log and its exact artifact directory, and search
supports workspace filtering, pagination, ranked title/content matches, and
optional snippets without a separate index service.

`x.ai/session/repair` recovers histories whose tool-call owner was lost to a
corrupt JSONL line. It removes orphaned, displaced, and duplicate tool results,
inserts synthetic failures for unanswered calls, and supports `dryRun`. Offline
sessions are replaced atomically; resident sessions are rejected while busy and
reset their provider history to a safe transcript after the repair is durable.

`x.ai/prompt_history` reads user prompts directly from the same append-only
session logs. Workspace history is returned newest-first, while `session_id`
queries preserve chronological prompt indices; `filter_session_id` provides a
newest-first session filter without maintaining a second history database.

`x.ai/pr/status` resolves a branch through GitHub CLI and reports open, draft,
closed, or merged state plus merge-queue membership. Missing CLI credentials or
an absent pull request degrade to a null result.

`x.ai/search/content` performs bounded local ripgrep searches using an explicit
`cwd` or a live session workspace. It supports literal, whole-word, regex,
case-insensitive, include/exclude-glob, ignore-file, file-limit, and match-limit
options, streams `x.ai/search/content/status` batches, and returns the complete
reference-shaped result.

ACP file pickers can open, change, and close stateful searches through
`x.ai/search/fuzzy/open`, `x.ai/search/fuzzy/change`, and
`x.ai/search/fuzzy/close`. Searches use smart-case subsequence ranking, honor
ignore and hidden-file modes, filter directories, preserve client request IDs,
cancel superseded queries, and publish bounded `x.ai/search/fuzzy/status`
notifications with relay routing metadata.

ACP code navigation reuses each session's configured language servers for
`x.ai/code/goto-definition`, `x.ai/code/goto-references`,
`x.ai/code/find-definitions`, `x.ai/code/find-references`, and
`x.ai/code/status`. Requests require an active session, accept editor-style
1-based positions, and return absolute paths with normalized 1-based ranges.

The `x.ai/session_summaries/session_list`, `workspace_list`, and
`workspace_list_recent` extensions expose the same logs as reference-shaped
summary snapshots for workspace history and recent-session views.

`x.ai/sessions/list` merges those persisted summaries with live ACP sessions
for a compact roster of working, idle, needs-input, and dormant sessions.
`x.ai/sessions/changed` publishes matching upsert/remove deltas when sessions
become resident, cross turn or interaction boundaries, or close. Resident rows
include the active model, reasoning effort, permission mode, and worktree state.

`x.ai/session/list` provides the local build-session lane with reference cursor
encoding, title/ID search, page limits, `kind`/`cwd` facet filters, and window
facet counts. Its conversations partial flag remains false because this build
has no cloud conversations backend.

`x.ai/session/close` performs idempotent live-session shutdown using the same
runtime and cancellation cleanup as the standard ACP close method.

`x.ai/internal/evict_sessions` unloads fully idle sessions when their client
disconnects while keeping sessions with active turns, queued input, background
work, scheduled tasks, or foreground commands resident and resumable.

`x.ai/btw` accepts a `sessionId` and `question`, answers from an isolated
snapshot without interrupting an active prompt, and returns
`{"result":{"answer":"..."}}`. Closing the session cancels and waits for the
side question without changing the main response chain.

`x.ai/interject` accepts text and optional image content while a turn is
running. Interjections enter the model loop as FIFO user messages at the next
safe point, are broadcast through `x.ai/session/interjection`, and fall back to
standalone prompt turns ahead of background wakes if they arrive after the
turn's final drain.

`x.ai/recap` accepts a `sessionId` and optional `auto` flag, immediately
acknowledges the request, then emits a display-only `session_recap` update with
the summary. Manual failures emit `session_recap_unavailable`; automatic
requests fail silently and run only after three turns, three idle minutes, and
a new turn since the last successful recap. Closing the session cancels and
waits for recap generation without changing the main response chain or history.

`x.ai/workspaces/list` returns the reference-compatible partial `no_oauth`
response because this local build has no cloud workspace backend.

Live ACP sessions expose their resolved skill catalog through
`x.ai/skills/list` and `x.ai/skills/config`, including scope, invocation gates,
plugin metadata, configured paths, ignore paths, and enabled state.
`x.ai/skills/refresh-baseline` and the internal skills reload endpoint
re-discover every live session's catalog from disk.

Live session MCP servers are exposed through `x.ai/mcp/list`, and
`x.ai/mcp/call` invokes a named server tool through the same client and
permission path used by model tool calls. `x.ai/mcp/read_resource` returns raw
text or base64 resource contents. Local servers and individual tools can be
persistently enabled or disabled with `x.ai/mcp/toggle` and
`x.ai/mcp/toggle_tool`; `x.ai/mcp/upsert` and `x.ai/mcp/delete` atomically
update the user config and hot-reload the live session. Disabled entries remain
visible in `x.ai/mcp/list`. `x.ai/session/update_mcp_servers` safely
restarts the session MCP runtime, preserves project/plugin base servers, and
restores the previous runtime if replacement fails. Sessionless agent-level MCP
pools and OAuth enrollment are not yet available. Local MCP configuration files
reload automatically without dropping client-provided session servers.
The internal global and project-scoped MCP reload endpoints refresh matching
live sessions from disk while preserving those client-provided overrides.
For local-only sessions, `x.ai/mcp/auth_status` reports no pending authentication
and `x.ai/mcp/auth_trigger` returns an explicit unsupported result.

The `x.ai/fs/list`, `exists`, `read_file`, `write_file`, and `delete_file`
extensions provide workspace-confined file access. Listing supports stable
dirs-first pagination, bounded depth, hidden/Git-ignore controls, glob filters,
and safe symlink traversal. Reads support UTF-8 or base64 byte ranges; writes
use same-directory atomic replacement and preserve existing file permissions.
Deletion is intentionally file-only and never recursively removes directories.

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

ACP clients can also run non-interactive commands through
`x.ai/terminal/create`, then inspect bounded merged output, wait for completion,
kill, release, or explicitly background the process. These terminals are
session-isolated, preserve direct argument vectors, report exit codes or Unix
signals, and stop with their session unless explicitly backgrounded.

Git repositories also expose compatible hunk tracker ACP extensions:
`x.ai/hunk-tracker/get-hunks`, `get-files`, `get-all-file-contents`,
`get-summary`, `hunk-action`, `file-action`, `turn-action`, and `all-action`.
Core Git extensions are available through `x.ai/git/git_repo_root`, `status`,
`current_commit`, `info`, `branches`, `stage`, `unstage`, `discard`, `stash`,
`stage/content`, `checkout`, `checkout_session_head`, `checkout_commit`,
`commit`, and `files`. Session HEAD checkout restores the commit recorded in a
persisted session, with optional dirty-state stashing and fetch fallback. Status
reports repository, branch/upstream, staged, unstaged, untracked, optional
line-count data, and optional per-file patches with byte and line counts using
the reference extension result envelope. Nested Git repositories and submodules
are ignored by default and can be included explicitly. `diffs` compares
commit-ish, staged, and working versions with optional patches and text content,
and rejects all files whose patches exceed requested byte or line limits.
Tracked, staged, and text untracked changes are included; mutations performed
through Gork file tools are attributed to the agent, while other changes are
reported as external. Attribution is per hunk, so user and agent edits in the
same file remain distinct, including after staging. Actions accept `accept` or
`reject`; `turn-action` targets agent hunks from one zero-based prompt index.
Accepted hunks are hidden for the current session, while rejection restores the
recorded old text only when the current line range still exactly matches the
hunk. A stale hunk fails closed instead of overwriting newer edits.

When `get-hunks` includes a path, its response also includes Git HEAD
`baseline` and on-disk `current` content views. The bulk content endpoint
returns the same views for every dirty path, including accepted hunks and
binary-only changes. Each view reports `missing`, `binary`, `tooLarge`,
`lfsPointer`, `symlink`, or `full`; text reads are bounded to 1 MiB. Legacy
`baselineContent` and `currentContent` fields remain for full text content.

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
- `--approval auto`: automatically approve edits, reads, searches, and routine
  local development commands; prompt for unknown, risky, external, or
  interactive actions. Unknown actions are evaluated by an isolated model call
  using bounded recent user/tool context and project instructions; unavailable
  or malformed classifier results fall back to the prompt.
- `--approval always-approve`: approve every unmatched action. Use only in a
  trusted workspace and environment. Managed requirements may disable this
  mode without disabling classifier-based `auto`.

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
explicit empty list blocks all fetches. The tool is registered only when enabled
by `[features] web_fetch = true`, `GROK_WEB_FETCH=true`, or authenticated remote
settings; environment and TOML values take precedence over remote settings.

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

`monitor` runs a background command whose stdout is delivered as real-time,
debounced events. It applies the reference line/batch limits, token-bucket rate
limit, sustained-overload stop, ACP notification drain, and completion-wake
deduplication. `scheduler_create`, `scheduler_list`, and `scheduler_delete`
manage up to 50 one-shot or recurring prompts; intervals have a 60-second
minimum, recurring tasks expire after seven days, and only `durable: true`
tasks are restored from session state. One-shot headless, REPL, TUI, and ACP sessions
fire scheduled prompts through a serialized synthetic-turn queue while
preserving the current response-ID chain; ACP also exposes
`x.ai/scheduler/delete`.

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

MCP configuration is also discovered from project `.grok/config.toml` files,
enabled plugin `.mcp.json` or inline `mcpServers`, `~/.claude.json`, project and
global Cursor `mcp.json`, and `.mcp.json` files from the Git root through the
workspace. Precedence is TOML, plugins, Claude, Cursor, then `.mcp.json`; closer
project files win within one source. `${VAR}` and `$VAR` are expanded without
removing unknown variables. Plugin MCP values additionally support the plugin
root/data substitutions used by plugin skills. CLI and ACP sessions poll these
local inputs by content and atomically reload MCP servers after changes while
preserving ACP client-provided server overrides.

MCP Streamable HTTP endpoints use `url` instead of `command`. Gork sends the
negotiated protocol and session headers, accepts both JSON and SSE responses,
and closes stateful sessions with DELETE:

```toml
[mcp_servers.remote]
url = "https://mcp.example.com/rpc"
headers = { Authorization = "Bearer token" }
# Or: bearer_token_env_var = "MCP_ACCESS_TOKEN"
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

LSP configuration is also discovered from `$GROK_HOME/lsp.json` (normally
`~/.grok/lsp.json`), the current workspace's `.grok/lsp.json`, and enabled
plugin `.lsp.json` or inline `lspServers` entries. Project entries override
configured TOML entries, which override user JSON; plugins only fill unclaimed
names. Release builds require folder trust before project LSP or project-plugin
LSP processes start.

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
The model-facing `skill` tool accepts the same optional `args` value, and both
paths accept qualified names such as `user:deploy` or `local:review`.
Additional compatible frontmatter is preserved for clients and future UI use:
`argument-hint`, `license`, `compatibility`, `allowed-tools`, `model`, `effort`,
and string-valued `metadata` entries including `short-description` and `author`.

Local plugin skills and commands are discovered from enabled plugins. A plugin
may use `plugin.json`, `.grok-plugin/plugin.json`, or
`.claude-plugin/plugin.json`; without a manifest, its directory name becomes
the plugin name and the conventional `skills/` and `commands/` directories are
used. Manifest component paths are confined to the plugin root after resolving
symlinks. Plugin skills use the directory basename as their identity and remain
available as `plugin-name:skill-name`; a frontmatter `name` is retained as the
display label. A unique plugin skill also has a bare alias, but a native skill
with the same name wins. Plugin bodies support `${GROK_PLUGIN_ROOT}`,
`${CLAUDE_PLUGIN_ROOT}`, `${GROK_PLUGIN_DATA}`, and `${CLAUDE_PLUGIN_DATA}`.

Explicit `[plugins].paths` entries are enabled automatically. Plugins found in
`$GROK_HOME/plugins`, `~/.claude/plugins`, `.grok/plugins`, or
`.claude/plugins` are disabled until their name or stable ID is listed in
`enabled`; `disabled` always takes precedence:

```toml
[plugins]
paths = ["~/my-plugins/deploy-tools"]
enabled = ["team-tools"]
disabled = ["old-tools"]
```

Enabled plugins may also contribute `.mcp.json`/inline `mcpServers` and
`.lsp.json`/inline `lspServers`. Development builds follow the reference's
unstamped-build behavior; release builds require folder trust before an enabled
project plugin may start MCP or LSP processes. ACP sessions expose enabled and
disabled inventory through `x.ai/plugins/list`, including scope, trust, skill,
agent, hook, and MCP summaries. `x.ai/plugins/action` persists local path add/remove and
plugin enable/disable, while reload re-runs discovery. Inventory and
skill/command components update immediately. `x.ai/plugins/notify-updates`
delivers durable, session-targeted installed-update notifications. Plugin MCP
servers are restarted with rollback on failure while preserving client-provided session overrides;
plugin LSP servers are started as a complete replacement set and atomically
swapped into the live manager. Supported local plugin actions therefore do not
require a session restart. `install`, `update`, and confirmed `uninstall`
actions manage isolated local/Git snapshots under
`$GROK_HOME/installed-plugins`, persist an atomic registry, and refresh live
skill/MCP/LSP/hook components. Multi-plugin repositories require explicit uninstall
confirmation. `x.ai/plugins/reload` forces local snapshot refresh and updates
all live plugin-backed components without requiring a session ID.

Enabled executable plugins may define `hooks/hooks.json`. Command and HTTPS
handlers receive the compatible camel-case JSON event envelope on stdin or as
the request body. `PreToolUse` is blocking only when a handler explicitly
returns `{"decision":"deny","reason":"..."}`; crashes, nonzero exits,
timeouts, malformed output, and request failures fail open. Matchers are Go/RE2
regular expressions over tool names. Command hooks receive authentic
`GROK_HOOK_*`, workspace, plugin-root, and plugin-data environment variables.
HTTP hooks reject non-HTTPS, private, link-local, metadata-range, and unsafe
redirect targets. Hook names disabled through ACP persist one per line in
`$GROK_HOME/disabled-hooks`.

ACP `x.ai/hooks/list` exposes loaded hook metadata and parse warnings;
`x.ai/hooks/action` supports reload, enable, disable, source toggles, and folder
trust changes plus confined custom-path add/remove. ACP sessions may also register
callbacks through `_meta["x.ai/hooks"]`: matching `PreToolUse` callbacks receive
concurrent, timeout-bounded `x.ai/hooks/run` reverse requests and may explicitly
deny the tool, while other events receive fire-and-forget `x.ai/hooks/event`
notifications. Missing, late, malformed, or unknown replies fail open; callback
envelopes include prompt and transcript correlation, bound tool payloads to
128 KiB, and registrations propagate to child agents. Global discovery reads
Claude settings, `$GROK_HOME/hooks`, `$GROK_HOME/hooks-paths`, and Cursor hooks;
trusted projects contribute the corresponding repo settings and `.grok/hooks`.
Vendor sources honor the `compat.<vendor>.hooks` gate. The runtime fires
`SessionStart`, `UserPromptSubmit`, `PreToolUse`, `PostToolUse`,
`PostToolUseFailure`, `Stop`, `StopFailure`, `PreCompact`, `PostCompact`, and
`SessionEnd` plus permission-prompt/denied, background task-completion
notifications, subagent start/stop, model/turn `agent_error`, and a cancellable
60-second post-turn `idle_prompt` notification.

Custom agent definitions are discovered from trusted project `.grok/agents`
and `.claude/agents` directories from the current directory to the Git root,
then `$GROK_HOME/agents`, legacy/user Claude locations, bundled agents, and
enabled plugin `agents/*.md`. Trusted project agents may shadow the built-in
`general-purpose`, `explore`, and `plan` types; user definitions cannot silently
replace a built-in, and plugin agents use `plugin-name:agent-name` identities.
Definitions may set `memory: user`, `memory: project`, or `memory: local`.
The corresponding `MEMORY.md` is injected at startup with a 200-line/25-KiB
cap, and that child alone receives standard read/write/replace access to its
agent-memory directory. User memory lives under `$GROK_HOME/agent-memory`;
project and local memory live under `.grok/agent-memory` and
`.grok/agent-memory-local` respectively.

The `task` tool runs these definitions through the existing Runner and parent
tool infrastructure. It supports foreground and background execution,
`get_task_output`, `kill_task`, completed-agent resume, per-agent turn limits,
model/reasoning-effort metadata, inherited context-window/compaction settings,
profile-aware model validation/routing, strict runtime-enum validation, tool
allow/deny lists, capability modes, isolated parent-skill snapshots, per-agent
skill preloading/discovery/inheritance controls, `all`/`none`/`named`/`except`
parent MCP inheritance filters, and subagent hook events. Plugin agents do not
inherit parent MCP servers. User and trusted-project agent definitions may add
private inline hooks; plugin inline hooks are ignored, and resumed agents retain
their original merged hook set. A fresh task may select another existing `cwd`;
workspace-bound tools are rebuilt for that directory while external adapters
that are independent of workspace state are shared, and resume keeps the source
task's effective cwd.
`isolation: worktree` creates a dirty-state-preserving linked worktree and
rebinds the same workspace tools there. Completion runs stop hooks, snapshots
the resulting tree to `refs/gork/subagents/<id>`, and removes the directory;
a later process can resume the child JSONL and rehydrate the same path from
that snapshot. Creation
failure falls back to the requested/shared cwd, while snapshot failure keeps
the worktree available instead of discarding changes.
Background tasks survive completion of the parent turn and are
cancelled during session cleanup. ACP exposes the typed `x.ai/subagent/get`,
`list_running`, and `cancel` methods; background terminal processes separately
use `x.ai/task/list`, `x.ai/task/kill`, `x.ai/task_backgrounded`, and
`x.ai/task_completed`; task lifecycle events are persisted in the parent JSONL
and replayed on ACP load. Running and completed subagent snapshots report real
turn, tool-call, token, context-usage, unique-tool, and error metrics. ACP also
pushes reference-shaped `subagent_spawned`, rate-limited `subagent_progress`,
and `subagent_finished` session notifications. Child histories and scoped
metadata are persisted per parent session; restart loads terminal results,
reconciles interrupted tasks once, and replays persisted lifecycle notifications.
ACP, headless, REPL, and TUI sessions queue successful or failed background
subagent and terminal-task completions as serialized
synthetic turns when `GROK_AUTO_WAKE` (or `[features] auto_wake`) is enabled;
blocking result consumption, timeouts, explicit cancellation, and session close
are coordinated so `will_wake` reflects an accepted queue entry.
User and trusted-project agent
definitions may attach named or inline `mcpServers`; owned servers override an
inherited server with the same name and remain private to that subagent.
Non-plugin agents may set `permissionMode: bypassPermissions` to skip interactive
approval, including after resume. Explicit deny mode and deny rules remain
authoritative, plugin agents cannot enable the bypass, and the managed
requirements lock described above downgrades the setting to normal prompting.

The same direct-install lifecycle is available outside ACP:

```sh
gork plugin install ./local-plugin
gork plugin install owner/repository@v1.2.0
gork plugin list
gork plugin update [plugin-name]
gork plugin uninstall [--confirm] [--keep-data] plugin-name
gork plugin marketplace list [--json]
gork plugin marketplace add <git-url-or-local-path>
gork plugin marketplace remove <git-url-or-local-path>
gork plugin marketplace update [source-name]
```

Local installs are full snapshots rather than symlinks. New sessions and
explicit plugin reloads safely recopy the source; uninstall removes plugin data
unless `--keep-data` is used. Git branches update with fast-forward-only pulls,
while version tags and commit SHA installs remain pinned.

Marketplace sources use the reference TOML shape:

```toml
[[marketplace.sources]]
name = "Team plugins"
path = "~/src/team-marketplace"

[[marketplace.sources]]
name = "Shared catalog"
git = "https://github.com/example/plugin-marketplace.git"
branch = "main"
```

ACP `x.ai/marketplace/list` scans `.grok-plugin/marketplace.json` (with
`.claude-plugin` compatibility) or falls back to `plugins/*`. The corresponding
action endpoint refreshes/adds/removes sources and installs, transactionally
updates, or uninstalls catalog plugins. Git sources use a persistent
`$GROK_HOME/marketplace-cache`; remote index entries may pin a tag or commit SHA.
The CLI manages the same source registry; removing a source also uninstalls its
plugins and clears their enabled/disabled settings. It also imports
`extraKnownMarketplaces` from `settings.local.json` and `settings.json`, then
`plugins/known_marketplaces.json`, under both `$GROK_HOME` and `~/.claude`.
Set `GROK_OFFICIAL_MARKETPLACE_AUTO_REGISTER=true` to register the official xAI
source once; removing it records that choice. Version 1 `plugin-index.json`
catalogs enrich indexed plugins with sanitized skill, command, agent, MCP, hook,
and LSP component inventories. The remote feature-flag gate is not yet implemented.

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

ACP clients can persist and apply these settings with `x.ai/skills/add`,
`remove`, `reset`, and `toggle`. Updates atomically rewrite the user config,
preserve unrelated tables, and rebuild all live session catalogs. New sessions
also use the updated settings.

Claude and Cursor discovery surfaces default to enabled. They can be disabled
independently in `config.toml`, with matching `GROK_<VENDOR>_<SURFACE>_ENABLED`
environment variables taking precedence:

```toml
[compat.cursor]
skills = false
rules = false
agents = false
mcps = false
hooks = false
```

## Privacy

Gork Go does not include product analytics, research trace uploads, repository
packaging uploads, or vendor auto-update code. Prompts and tool results used by
the agent are sent to the configured model endpoint because remote inference
requires them. Session records stay local unless the user moves or uploads
them.

## License and attribution

Apache License 2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
