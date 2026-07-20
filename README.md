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
the current completed response chain and continue from a fresh context. They
also accept `/loop [interval] <prompt>`, which expands to the reference
`scheduler_create` workflow without inventing a default interval. ACP advertises
both commands through `x.ai/commands/list`.

The model can call `enter_plan_mode` to enter a persisted, read-only planning
phase. While active, workspace mutations are limited to `.grok/plan.md`; shell,
background, scheduler mutation, image-generation, task, monitor, and MCP tools
are blocked. `exit_plan_mode` presents that file for approval, including the
ACP `x.ai/exit_plan_mode` reverse request, and applies the decision before the
next model step in the same turn.

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

The TUI streams model output as it arrives, renders headings, emphasis, links,
lists, quotes, and inline or fenced code, and keeps a scrollable transcript.
It supports Unicode input, displays tool status, cancels the current turn with
Ctrl-C, and presents write/Shell/MCP approval prompts inside the alternate
screen. Shift-Tab toggles persisted Plan mode and shows a visible mode badge.
Plan exits open a dedicated full-plan review with approve, revision-feedback,
and abandon outcomes. Page Up/Page Down scroll, Ctrl-Q exits, and an optional
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

Progress-only `update_goal` calls keep the goal active. A completion claim
starts three independent, read-only `general-purpose` skeptics. Two refutations
return the goal to the active loop with their concrete gaps; malformed verdicts
and individual skeptic failures count as refutations, while an unavailable
verifier backend fails open so an internal harness outage cannot strand the
user. Goal mode is explicit and cannot be combined with the interactive REPL
or TUI. The panel defaults to three skeptics. `[goal] verifier_count`, remote
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
role. `--goal --resume <session.jsonl>` reactivates an unfinished goal,
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
config is present; headless and ACP sessions fail closed. `--trust` records the Git
workspace in `$GROK_HOME/trusted_folders.toml` (normally
`~/.grok/trusted_folders.toml`). Parent trust cascades to child paths while a
more specific child decision wins. Development versions such as `0.1.0-dev`
match the reference's unstamped-build behavior and keep this gate inert.

```sh
./gork --trust --workspace /path/to/project "inspect this repository"
```

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

Persisted and live sessions also expose `x.ai/session/info`, `rename`,
`delete`, and `search`. Rename appends an explicit title event, delete removes
only the validated JSONL log and its exact artifact directory, and search
supports workspace filtering, pagination, ranked title/content matches, and
optional snippets without a separate index service.

`x.ai/prompt_history` reads user prompts directly from the same append-only
session logs. Workspace history is returned newest-first, while `session_id`
queries preserve chronological prompt indices; `filter_session_id` provides a
newest-first session filter without maintaining a second history database.

The `x.ai/session_summaries/session_list`, `workspace_list`, and
`workspace_list_recent` extensions expose the same logs as reference-shaped
summary snapshots for workspace history and recent-session views.

`x.ai/sessions/list` merges those persisted summaries with live ACP sessions
for a compact roster of working, idle, and dormant sessions.

`x.ai/session/list` provides the local build-session lane with reference cursor
encoding, title/ID search, page limits, `kind`/`cwd` facet filters, and window
facet counts. Its conversations partial flag remains false because this build
has no cloud conversations backend.

`x.ai/session/close` performs idempotent live-session shutdown using the same
runtime and cancellation cleanup as the standard ACP close method.

`x.ai/workspaces/list` returns the reference-compatible partial `no_oauth`
response because this local build has no cloud workspace backend.

Live ACP sessions expose their resolved skill catalog through
`x.ai/skills/list` and `x.ai/skills/config`, including scope, invocation gates,
plugin metadata, configured paths, ignore paths, and enabled state.

Live session MCP servers are exposed through `x.ai/mcp/list`, and
`x.ai/mcp/call` invokes a named server tool through the same client and
permission path used by model tool calls. `x.ai/mcp/read_resource` returns raw
text or base64 resource contents. `x.ai/session/update_mcp_servers` safely
restarts the session MCP runtime, preserves project/plugin base servers, and
restores the previous runtime if replacement fails. Sessionless agent-level MCP
pools and OAuth enrollment are not yet available. Local MCP configuration files
reload automatically without dropping client-provided session servers.
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

Git repositories also expose compatible hunk tracker ACP extensions:
`x.ai/hunk-tracker/get-hunks`, `get-files`, `get-all-file-contents`,
`get-summary`, `hunk-action`, `file-action`, `turn-action`, and `all-action`.
Core Git extensions are available through `x.ai/git/git_repo_root`, `status`,
`current_commit`, `info`, `branches`, `stage`, `unstage`, `discard`, `stash`,
`stage/content`, `checkout`, `checkout_session_head`, `checkout_commit`,
`commit`, and `files`. Session HEAD checkout restores the commit recorded in a
persisted session, with optional dirty-state stashing and fetch fallback. Status
reports repository, branch/upstream, staged, unstaged, untracked, and optional
line-count data using the reference extension result envelope. `diffs` compares
commit-ish, staged, and working versions with optional patches and text content.
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
skill/command components update immediately. Plugin MCP servers are restarted
with rollback on failure while preserving client-provided session overrides;
plugin LSP servers are started as a complete replacement set and atomically
swapped into the live manager. Supported local plugin actions therefore do not
require a session restart. `install`, `update`, and confirmed `uninstall`
actions manage isolated local/Git snapshots under
`$GROK_HOME/installed-plugins`, persist an atomic registry, and refresh live
skill/MCP/LSP/hook components. Multi-plugin repositories require explicit uninstall
confirmation.

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
trust changes plus confined custom-path add/remove. Global discovery reads
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
ACP sessions queue successful or failed background subagent and terminal-task
completions as serialized
synthetic turns when `GROK_AUTO_WAKE` (or `[features] auto_wake`) is enabled;
blocking result consumption, timeouts, explicit cancellation, and session close
are coordinated so `will_wake` reflects an accepted queue entry.
User and trusted-project agent
definitions may attach named or inline `mcpServers`; owned servers override an
inherited server with the same name and remain private to that subagent.
Non-ACP background-task/subagent auto-wake and `bypassPermissions` execution
are not implemented yet.

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
