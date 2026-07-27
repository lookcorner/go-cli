# Gork Build compatibility

This file tracks behavioral compatibility against
`thedavidweng/gork-build@b875c32e283ca04eee36ddeec1f3886be9f3f21a`.

Status values: **done**, **partial**, **planned**.

The reference release matrix's six desktop targets are enforced continuously:
Linux, macOS, and Windows on amd64 and arm64. CI also gates formatting, the full
test suite, the race detector, vet, and the native build.

Trusted workspace roots load `.envrc` through `direnv export json` when
available and otherwise through a Bash subshell with common direnv helpers;
both are Unix-only paths, so Windows workspaces silently skip `.envrc`.
The default-on `[session].load_envrc` gate, fail-open execution, current-root
scope, folder-trust enforcement, and explicit environment precedence match the
reference behavior.

ACP prompt queuing includes server-authoritative busy-turn FIFO execution,
`x.ai/queue/changed` snapshots, versioned owner-aware remove/reorder/clear/edit
and send-now operations, user-prompt priority over automatic wakes, and
close-time cancellation of queued requests. Completed prompts publish ordered
`x.ai/session/prompt_complete` notifications and correlated `_meta` response
metadata with model/token/turn/cancellation details; removed queued prompts do
not publish false completion events. Context-aware `x.ai/compact_conversation`
with persisted completion updates and persisted fire-and-forget
`x.ai/toggle_plan_mode` are also supported. ACP remains **partial** overall.
ACP clients can negotiate session-bound real-time workspace events or a
Gitignore-aware chunked file index with add/remove deltas.
Live session rosters publish `x.ai/sessions/changed` deltas for resident,
working, idle, needs-input, and removed transitions.
Opted-in ACP sessions publish deduplicated `x.ai/git_head_changed` branch and
linked-worktree identity updates at startup, after mutation tools, and for
external HEAD changes.
Release ACP clients can also negotiate `x.ai/folderTrust.interactive`; gated
sessions request a fail-closed trust decision once per workspace and hot-reload
their executable project components after an explicit grant.
Skill catalogs support all-session `x.ai/skills/refresh-baseline` and
`x.ai/internal/reload_skills` / `x.ai/internal/reload_workflows` disk refreshes.
`x.ai/workflows/list` discovers builtin/project/user `.rhai` workflow meta
(lightweight map parse; Rhai execution remains).
`x.ai/mcp/setup` persists select-field answers in `$GROK_HOME/mcp_preferences.json`,
resolves `setup` templates (v0 one-select), reloads MCP, and enables the server.
`x.ai/mcp/list` also surfaces unresolved setup schemas as `setuprequired`
placeholders with `setup` / `setupValues` fields, including executable plugin
MCP sources (`sourceLabel: plugin: <name>`).
ACP command discovery always exposes plugin skills by `plugin:name`, adds the
bare name only when unambiguous, and preserves native or built-in bare commands.
Plugin extensions also support sessionless forced `x.ai/plugins/reload` with
live component fan-out.
ACP bulk history now includes rewind-filtered, paginated/tail/turn-indexed
`x.ai/session/updates` responses with prompt boundaries, event cursors,
multimodal/tool/lifecycle projection, and routed streaming chunks.
New and loaded ACP sessions share reference-shaped `x.ai/sessionConfig` option
metadata and `x.ai/sessionDetail` identity/title metadata.
Loaded sessions honor explicit `_meta.x.ai/restore_code` requests and report
reference-shaped `_meta.codeRestore` checkout results.
New sessions expose their working directory and Git-root state; loaded sessions
echo client-owned `x.ai/persist` metadata and their session ID.
Loaded ACP sessions also report `gitDivergence` when their recorded Git HEAD
differs from the workspace's current HEAD.
ACP session deletion uses the same cancellation-and-drain shutdown path as
session close before removing persisted history.
ACP `_meta.x.ai/display_cwd` metadata is persisted across restore and used for
active or dormant session roster display. Live and replayed ACP updates rewrite
plain and URL-encoded worktree paths for the client while the real cwd remains
the execution path; hunk tracker paths also round-trip between display and real
workspace roots. Session startup metadata and the model's one-time workspace
context use the display path without rebinding workspace tools.
Authenticated remote-settings refreshes publish reference-shaped
`x.ai/settings/update` notifications so connected ACP clients receive live access,
subscription, permission, layout, tip, and announcement state.
Authenticated ACP clients can list, create, update, and delete remote sandbox
environments and terminate sandbox sessions through the reference cloud extensions.
Rollout survey submissions validate the reference payload and acknowledge receipt.
Remote-enabled authenticated clients can also export persisted session updates,
sync them to the code backend, and create reference-shaped public share URLs.
ACP announcement updates also use reference-shaped `x.ai/announcements/update`
notifications with expiry filtering, change deduplication, epoch-seeded monotonic
generations, initialize/new-session replay semantics, and authenticated periodic
remote refreshes.
TUI and interactive sessions render the session announcement slot for `critical`
and `promo` entries, prioritize critical notices, filter expiry, and support
persisted per-notice `/announcements hide|show` controls.
Authenticated ACP bundle sync/status/entry extensions populate the shared bundled
persona, role, agent, and skill cache through bounded archives or legacy JSON,
preserve local modifications, retry refreshed session credentials, and refresh
live skill catalogs after successful updates.
TUI, interactive REPL, and ACP sessions expose the always-on `/session-info`
command with reference `/status` and `/info` aliases, completed-turn counts,
and latest context usage without starting a model turn. ACP also advertises and
locally completes the reference display-only `/context` command.
ACP `/always-approve` and `/yolo` commands apply reference on/off argument
semantics locally while preserving explicit-deny and managed-policy locks.
ACP `/goal` advertises the reference management actions and trailing positive
token-budget syntax; status, pause, and clear complete locally, while create and
successful resume continue through inference and publish live Goal updates.
Hook-capable ACP sessions advertise and locally execute the reference
`/hooks-list`, `/hooks-trust`, `/hooks-untrust`, `/hooks-add`, and
`/hooks-remove` commands without starting a model turn. Command discovery also
preserves the reference ordering across the implemented memory, Hook, Goal, and
scheduler groups.
Pre-tool hook denials return their reason as the blocked tool result so the
model can adapt or retry without cancelling the active turn.
Feedback-capable ACP and TUI sessions advertise `/feedback`; submissions are
persisted locally without a model turn and deliberately use the reference
`LocalOnly` outcome because this privacy build does not configure a feedback
upload service.
The corresponding `x.ai/feedback` extension accepts simple and structured
rating submissions with reference clamping and turn fallback. Dismissals are
persisted before returning the reference missing-credentials error.
`x.ai/review/comment` and `x.ai/review/comment/delete` return reference-shaped
responses with UUIDv7 identities while persisting only local citation/create
and deletion-tombstone events; cloud review uploads are intentionally absent.
`x.ai/debug/trigger_feedback` emits the reference feedback-request notification
and response shapes locally for client integration testing.
`x.ai/debug/agent` returns process-local registry counts for client engineers.
The TUI also implements the reference hidden `/debug [scroll|fps|log]`
diagnostics: live scroll and FPS overlays, the `/scroll-debug` alias, bare
toggle-state reporting, environment startup switches, and an event-exact JSONL
recorder for the Go TUI's discrete keyboard and wheel scroll transitions.
Responses reasoning summaries, Chat Completions `reasoning_content`, and
Anthropic thinking blocks stream through a separate agent output, persist as
display-only session events, and render in distinct TUI Thinking blocks.
`[ui].show_thinking_blocks` defaults on and the live `/settings` toggle rebuilds
completed scrollback so hidden thoughts disappear and can be restored without
entering model history or Markdown exports. `[ui].max_thoughts_width` defaults
to 120, clamps to 40-500 display columns, persists atomically, and applies
immediately to live and restored thinking blocks.
The default-on `[ui.contextual_hints].image_input` gate performs one bounded
clipboard check when an idle, unobscured TUI regains focus; pasteable PNGs show
the reference Ctrl-V hint without attaching data, with content deduplication and
a 30-second fire cooldown.
Voice STT accepts TLS, WSS, or scheme-less API bases, inherits the active model
endpoint, and de-duplicates existing `/v1` path prefixes.

REPL, TUI, and ACP sessions support shared `/usage` and `/cost` billing metrics
plus management links as local commands that never invoke model inference. ACP
also advertises `/usage` with `show | manage` argument guidance.
Capability-gated `/share` is also available across REPL, TUI, and ACP without
model inference. It reuses the `x.ai/share_session` domain service, refreshes an
expired token once, preserves the reference 413 data-upload fallback, and
enforces authentication, account enablement, and team ZDR restrictions.
`/release-notes` and its `/changelog` alias fetch the reference per-version
Markdown changelog with a three-second timeout, cache successful responses under
`$GROK_HOME`, fall back when offline, and complete locally across REPL, TUI, and
ACP without model inference.
REPL, TUI, and ACP sessions expose `/imagine <description>` only when
`image_gen` is registered and `/imagine-video <description>` only when
`image_to_video` is registered. They preserve the slash command in session
history while replacing the model input with the reference direct image or
image-first video workflow; empty descriptions complete locally with usage.
The side-effect-free top-level `gork completions` command emits Bash, Elvish,
Fish, PowerShell, and Zsh scripts for the currently implemented command tree.
The top-level `gork update` command matches the privacy build's hard-off policy:
`--check` and reference-shaped JSON status report auto-update disabled without
probing vendor endpoints, stable/alpha/enterprise channel switches persist
atomically, and every install or forced/pinned update path returns community
source-build and Releases guidance without downloading or replacing the binary.
The top-level `gork trace` command preserves the privacy build's hard-off for
trace uploads while providing reference-shaped local `.tar.gz` exports, JSON
output, bounded related session artifacts, and non-sensitive export metadata.
The top-level `gork wrap` command runs arbitrary commands in a local Unix PTY
or Windows ConPTY, forwards terminal input and resize events, intercepts plain
and tmux-wrapped OSC 52 writes into the native clipboard, observes latched DEC
private modes and kitty keyboard pushes so dirty child exits emit matching
restores while clean exits stay byte-transparent, answers the private host
clipboard-image OSC with bracketed `GROK_WRAP_IMG`/`GROK_WRAP_NONE` frames for
remote paste-miss recovery (JPEG-recompressing oversized host screenshots under
the 20 MiB wrap cap), and preserves output and exit status;
non-interactive and unsupported-platform runs fall back to direct execution.
Common reference run flags are supported, including cwd/model/reasoning,
single-prompt, system-prompt/rules, turn-limit, permission-mode,
always-approve, version, and session-startup aliases. `--resume/-r [ID]`,
`--load ID`, and `--continue/-c` support strict ID/path resume or the current
workspace's most recent session; `--session-id/-s UUID` names a new session,
and `--fork-session` creates an append-only child without changing its parent.
`--worktree/-w [LABEL]` and `--worktree-ref/--ref REF` create isolated
Git-backed startup sessions; combining worktree mode with resume copies the
parent conversation into a child bound to the isolated workspace.
Session-scoped `--no-plan`, `--no-subagents`, `--no-ask-user`, and
`--disable-web-search` capability switches remove their corresponding runtime
tools without rewriting configuration; disabling web search also removes web
fetch.
Explicit CLI values are reapplied after remote settings so command-line intent
wins.
Model HTTP 429 errors retain structured status at the API boundary and use the
active authentication method for user-facing guidance: API-key sessions point to
team credits and API rate-limit tiers, while OAuth sessions use personal-plan copy.
Headless prompt files, ACP JSON text/image/resource blocks,
plain/JSON/streaming-JSON output, and JSON Schema structured output are
supported. Schemas map to
Responses `text.format`, Chat Completions `response_format`, and Anthropic
Messages `output_config.format`.
The reference `gork agent stdio` command routes its supported model, reasoning,
permission, workspace, session, trust, memory, and capability flags through the
same complete ACP stdio runtime as `--acp`. `--agent-profile PATH` is
canonicalized and parsed at startup, then its primary-session prompt, model,
effort, maximum turns, permission bypass, tools, skills, MCP, hooks, and scoped
memory semantics are applied across direct stdio, serve, and leader runtimes.
Repeatable `--plugin-dir` paths
inject always-enabled, trusted process-local plugins at highest priority for
direct stdio and serve runtimes, while leader-backed sessions warn and ignore
them. `gork agent serve` exposes that
persistent runtime over an authenticated WebSocket and preserves active
sessions across reconnects. On Unix and Windows, `gork agent leader` exposes
the same ACP application through the reference framed local IPC protocol with
lock/socket lifecycle, readiness signaling, client request-ID isolation,
session ownership, disconnect cleanup, and optional persistent operation;
Windows uses a deterministic named pipe derived from the socket marker path
with a share-mode file lock in place of `flock`. Remote headless relay
mode remains unavailable and fails explicitly rather than being misparsed as a
prompt. Unix and Windows stdio followers adopt an existing leader or
coordinate a single background leader spawn when `--leader` or
`[cli] use_leader = true` is set;
`--no-leader` has highest precedence and keeps the direct ACP runtime.
Voice dictation supports the reference default `hold` capture mode on terminals
with key-release reporting, runtime fallback to `toggle` elsewhere, and
rollback-safe live `/settings` persistence of `[ui].voice_capture_mode` plus
the default-on `[ui].voice_keybind_enabled` Ctrl-Space/F8 gate without disabling
`/voice`. The default-on `[ui.contextual_hints].ssh_wrap` gate shows the
reference one-shot `/doctor` discovery hint only for ordinary unwrapped SSH
sessions, defers behind other hints, and pauses its ten-second lifetime while
occluded.
The TUI accepts Bubble Tea v2 bracketed-paste events as one undoable edit,
preserves Unicode, multiline and trailing-newline content, normalizes bare
carriage returns, and suppresses typed-only contextual nudges for pasted text.
PageUp and PageDown scroll the conversation without leaving the prompt in idle
or running turns, preserve the reference two-row overlap between pages, and
leave an open slash menu with first priority for paging.
The dashboard new-agent composer has its own session-local multiline mode via
`Ctrl+M`, `/multiline`, or `/ml`, with Enter and Shift/Alt-Enter send/newline
semantics independent from the active session prompt.
The dashboard shortcut panel uses `j` and `k` for scrolling only when Vim mode
is enabled; arrow, page, and home/end navigation remain available in both modes.
Linux and BSD browser launches fail fast without a display server or explicit
`BROWSER`, allowing TUI, ACP, and terminal commands to show the full URL instead.
On Linux X11/XWayland, an unmodified middle-button press reads PRIMARY through
`xclip` or `xsel` exactly once; Ctrl-V remains isolated to CLIPBOARD.
Plain TUI exits print a pasteable session resume command after terminal restore,
including `--minimal` when the active session used native scrollback mode.
Ctrl-V reads native clipboard text or validated PNG data, with Alt-V as a
Windows fallback. Draft images support image-only turns, queued prompts,
send-now interjections, compact attachment counts, and empty-composer Esc
clearing.
The top-level `gork leader list/info/kill` commands discover reference lock/socket
candidates, verify live Unix sockets or Windows named pipes with the reference
length-prefixed registration/control protocol, classify stale/unreachable/unsupported entries,
select the production or explicit-PID leader, emit reference-shaped JSON, and
terminate only identity-verified gork processes while cleaning stale candidate
files. Remote relay forwarding and workspace exposure remain.

| Area | Status | Current behavior / remaining work |
| --- | --- | --- |
| Responses API streaming | done | SSE text deltas, terminal response IDs, function calls and JSON fallback |
| Headless agent loop | done | Multi-step model/tool loop with cancellation and a configurable limit; repeated identical tool-call steps receive the reference eighth-call correction and end cleanly after sixteen repeats, while repeated `true` shell no-ops end after four; JSON and streaming-JSON final records aggregate uncached/cache-read/output/reasoning token usage across every model call, preserve per-model call totals, and expose exact server-reported USD ticks plus converted totals only when cost coverage is complete, with partial costs flagged instead of understated |
| Workspace file tools | partial | Gork-compatible standard `read_file`/`grep`/`search_replace` and configurable `hashline_read`/`hashline_grep`/`hashline_edit` toolsets cover anchor-validated atomic batches, stale/overlap rejection, bottom-up application, fresh anchors, CRLF and mode preservation, permission and rewind checkpoints, text/PPTX reads, PDF text extraction and 150-DPI page-image rendering with page ranges, validated PNG/JPEG/GIF/WebP reads with native Responses/Chat/Anthropic image content, negative offsets, Gitignore-aware bounded directory trees with large-subtree extension summaries, ripgrep filters, atomic create/overwrite, exact/all replacement and safe Unicode typography fallback; remote-gated path-not-found hints add CWD reminders, dropped-workspace corrections, and bounded similar-name suggestions across standard/hashline tools and live ACP sessions; PDF image rendering uses Poppler's `pdftoppm` |
| Tool permissions | partial | Prompt/classifier-auto/always-approve/deny modes, deterministic auto fast paths for edits, reads, searches, and routine local development commands, fail-closed isolated live-model classification with bounded recent user/tool context and project instructions, all four reference prompt variants, dedicated model/reasoning routing, field-wise local/remote `[auto_mode]` configuration, fresh/resumed subagent inheritance, live ACP ask/always-approve/auto switching with reference signal precedence, default-off config/environment/remote/requirements-gated request-scoped remembered ACP approvals, reference-compatible first-prompt default selection plus session-sticky TUI choice kinds, and local mutually exclusive `/always-approve` and gated `/auto` toggles plus ACP `/yolo` command handling work; immutable explicit-deny, requirements-managed always-approve, and runtime auto-mode gates remain authoritative, repeatable CLI rules and compatible `[permission].rules` cover any/bash/edit/read/grep/MCP/WebFetch (glob and domain) with deny→ask→allow precedence, and user/system requirements, administrator-forced macOS MDM, deployment/team remote sync, compile-time-keyed Ed25519 fail-closed cache verification, and release-build folder trust for repo MCP/plugin execution work |
| Shell execution | partial | Gork-compatible tools support foreground exit status, configurable `[toolset.bash]` default timeout and output-byte ceiling, background timeout/lifecycle, multi-task wait/poll, process groups, cleanup, replayed cwd/environment/functions/aliases and Bash/Zsh shell options with reference nounset/nonomatch safeguards; ACP session-scoped piped terminal create/output/wait/background/release/kill with bounded merged output and interactive PTY create/input/load/resize/list/kill, bounded replay and foreground-process activity transitions work; top-level `gork wrap` provides Unix PTY and Windows ConPTY raw-mode/resize forwarding, bounded streaming OSC 52 and tmux passthrough interception, native clipboard writes, latched DEC-mode/kitty-keyboard dirty-exit restore, host clipboard-image paste over the private OSC/`GROK_WRAP_IMG` path, direct fallback, and exit-code preservation; Linux cgroup v2 memory.high/max best-effort limits for model-started foreground/background shell children and ACP PTY/piped terminals with inotify memory.events OOM signaling that kills the newest running shell or ACP terminal child as exit 137/`oom` (skip when unavailable; cpu.max remains deferred to match reference) |
| Session persistence | partial | Durable corruption-tolerant JSONL events heal torn append tails, latest/path resume, completed-turn response-ID continuation, checkpoint-aligned transcript reconstruction, append-only conversation branches, validated session forks with malformed-line cleanup and prompt truncation/model override, top-level `gork sessions list/search/delete`, Markdown `gork export` to stdout/file/native clipboard, and authenticated remote `gork share <session-id>`, TUI `/fork` restart into current-directory or isolated-worktree children with inclusive zero-based `--at` truncation, bounded read-only TUI discovery of recent same-workspace Claude Code and Codex sessions from file rollouts or the highest supported SQLite state index, plus Cursor Desktop sessions from its global composer-header database, when the matching `/resume-*` skill and `compat.<vendor>.sessions` gate are enabled, plus a blank-startup `Ctrl-U` nudge for supported sessions updated within ten minutes, out-of-band idempotent tool-pairing repair with dry-run and atomic offline/live-logger persistence, and CLI/TUI/ACP rewind support for `all`, `conversation_only`, and `files_only`/`code_only`; Cursor CLI session discovery remains unverified, while conversation-only branches fold later file effects into the surviving checkpoint and file-tool/shell-command checkpoints are durable, conflict-aware and force-restorable across Responses, Chat Completions and Anthropic histories |
| Configuration | partial | Gork-compatible `$GROK_HOME`/`~/.grok/config.toml`, system/user `managed_config.toml` deep-merge precedence, per-layer inclusive semver `version_overrides`, model selection/custom providers, atomic user-default model updates, top-level `gork models`, redacted `gork inspect` text/JSON reports including all 13 supported Cursor/Claude/Codex compatibility cells with effective values, env/config/default provenance, and explicit one-shot remote-settings status, guarded reference-version/TTL/auth/origin `models_cache.json` catalog loading plus authenticated cache-miss `/models` fetch with tolerant entry parsing and private atomic writes, MCP/LSP tables, legacy JSON, environment and CLI layers, typed `[toolset] file_toolset`, `[toolset.hashline]` scheme/hash/chunk settings, `[toolset.bash]` timeout/output bounds, and `[toolset.web_fetch]` proxy/domain/explicit-loopback controls, typed `[auto_mode]` gate/prompt/model/reasoning settings, default-on grouped tool verbs, default-off remembered tool approvals and collapsed edit blocks, default-selected permission with environment override, reference permission startup precedence across canonical `permission_mode` and legacy `approval_mode`/`yolo` keys with fail-safe `ask` fallback, requirements/remote/user/managed startup-tip source ordering with local default exclusion and `[cli].show_tips` opt-out, and default-on feedback gating with environment, requirements, local config, and tolerant remote precedence work; user/system/macOS MDM requirements precedence includes sticky `[ui] disable_bypass_permissions_mode = true` and legacy `yolo = false` locks scoped only to requirements, plus `gork setup`/`--json`, deployment/team remote sync with transient retries and hard-stale startup repair, exact signed-payload matching, atomic cache/sidecar writes, offline fail-closed verification, `[folder_trust]`/`GROK_FOLDER_TRUST`, `--trust`, atomic trust persistence, ancestor cascade and linked-worktree key collapse, plus `[ui]` text-selection (including legacy bool/duration/double-click migration), separator, mouse-reporting-toggle, Vim-mode, compact-mode, default-on message timestamps and `page_flip_on_send`, bounded `max_thoughts_width`, canonical terminal theme with configurable automatic dark/light mappings, reference-scaled scroll speed, strict auto/wheel/trackpad mode with environment override, scroll-line, inverted-scroll, default-on prompt-suggestion, tri-state cursor-blink, canonical voice STT language with system-locale resolution, restart-scoped display-refresh auto cadence with macOS/Windows primary-display probes and safe fallback, and CLI/environment/config hunk-tracker modes work; remaining UI/toolset fields remain |
| Authentication | partial | API-key bearer auth plus cryptographically verified OIDC browser PKCE and xAI-compatible OAuth device login with browser/headless fallback, scoped login/logout and crash-safe `auth.json` storage, environment-or-stored API-key fallback for regular sessions and tools when OAuth is unavailable, best-effort remote profile enrichment and post-team-login policy sync, external command token mint/refresh, fail-closed preferred-method and single/multi-team pins, team principal preselection, existing-field preservation, JWT identity hints, proactive refresh, cancellable cross-process refresh/write locking with stale recovery, single-retry reactive 401 recovery across model backends, and ACP initialization method/default advertisement, standard `authenticate` handling for API-key/cached/browser/device/external-provider paths, one-shot `x.ai/auth/get_url`, pasted-callback `x.ai/auth/submit_code`, idempotent `x.ai/auth/cancel` (optional `request_seq` scoping), runtime credential/model-cache switching, `x.ai/auth/info` profile/team metadata, refresh-aware `x.ai/auth/getBearerToken`, current/named-scope `x.ai/auth/logout`, external-store `x.ai/internal/auth_cleared` runtime cleanup, exact-tier `x.ai/auth/check_subscription` with identity-targeted settings, fail-closed `allow_access`, AuthMeta gates, double OAuth refresh and JWT-tier-matched model-catalog retry, legacy environment-backed `x.ai/getApiKey`/`x.ai/setApiKey` persistence, and locked-opt-out `x.ai/privacy/setCodingDataRetention` with authenticated retry work |
TUI authentication note: `/login` and `/logout` reuse the existing OAuth/credential service, and `/home`/`/welcome` request a fresh session.
Persona editing note: user/project persona fields can be edited and safely renamed in the TUI; bundled personas remain read-only.
Turn-cancel note: when background subagents are running, the TUI supports the reference `ask`, `always_stop`, and `always_continue` policies, including one-shot and persisted choices; foreground subagents remain scoped to the parent turn context.
Cancel-rewind note: default-on `[features] cancel_rewind` / `GROK_CANCEL_REWIND` / remote `cancel_rewind_enabled` restores a pristine in-flight prompt on Ctrl+C before first model or tool activity in the TUI, and ACP `session/cancel` with `_meta.rewindIfPristine` trims the JSONL prompt while answering the prompt RPC as `cancelled` without publishing `x.ai/session/prompt_complete`.
Contextual-hint note: idle typed prompts now use reference-compatible whole-word planning-intent detection to show the default-on persisted `Planning? Check out plan mode via shift+tab` nudge at most three times per session; local, requirements, remote, and `GROK_CONTEXTUAL_HINTS` precedence are covered by tests.
Small-screen hint note: the first measured terminal height consumes a once-per-session evaluation; rows 21–28 show the default-on persisted `Tight on space? Try /compact-mode` ambient hint when user compact mode is off, with prompt-submit survival and occlusion-paused visible TTL.
Word-select hint note: the second click on assistant text while `flash` or `hold`
selection is active shows the default-on persisted settings hint at most three
times per session. Its 20-second visible TTL pauses under occlusion, prompt
edits retire it, and tip-scoped `Ctrl+Y` atomically persists `word_select`.
Context usage note: `/context` is an instant local view in TUI, REPL, and ACP
sessions. It keeps server-reported total usage authoritative while adding
bytes-per-four estimates for the system prompt, completed messages, current
tool definitions, model-visible skills, and MCP-owned tool definitions; the
same structured breakdown is returned by `x.ai/session/info`.
Cumulative session token/cost totals are available via `x.ai/session/usage`
(PromptUsage wire shape; costs scrub when partial).
| Chat Completions backend | done | Streaming text, incremental tool calls, tool-result messages, image content and process-local multi-turn history are covered by protocol tests |
| Anthropic Messages backend | done | Streaming content blocks, incremental JSON tool input, tool results, base64 image blocks, headers and process-local history are covered by protocol tests |
| Interactive UI | partial | Multi-turn REPL with capability-gated `/imagine` and `/imagine-video` workflow injection, local `/privacy` status, locked-opt-out confirmation, and top-level `gork doctor [--json]` plus `gork doctor fix [ssh-wrap|tmux-clipboard|dcs-passthrough|tmux-extended-keys|tmux-truecolor|colorterm] [--yes]` managed SSH-wrap alias, COLORTERM export, and tmux option installation for Bash/zsh/fish plus `/doctor` (`/terminal-setup`) report/list/fix slash handling with `--yes` apply across REPL/TUI/ACP plus environment/clipboard diagnostics with live tmux set-clipboard/allow-passthrough/extended-keys/control-mode probes, SSH-wrap recommendations, byobu-screen warnings, `NO_COLOR` and Apple Terminal truecolor/OSC-52 findings, iTerm2 OSC 52 permission and VS Code-family SSH non-ASCII copy recommendations, SSH/container OSC 52 delivery-unverified and delivery-unavailable clipboard findings (including container-without-display detection), Voice microphone probe with `voice.no-input-device` when capture is supported but no input is found, VTE/xterm.js Shift+Enter newline-fallback recommendations, default `[ui.notifications]` BEL-fallback and focus-tracking warnings, limited-color and tmux-256color/truecolor automatic-fix recommendations plus direct `! <command>` workspace-scoped Shell execution, `/copy [N]` assistant-response clipboard copying, Markdown `/export [filename]`, busy-turn FIFO prompt queuing with instant read-only `/queue` snapshots, empty-composer Enter injection of the oldest queued follow-up into the active turn (including multiline mode), a default-on persisted three-show send-now hint with a shared `GROK_CONTEXTUAL_HINTS` master gate and elapsed turn time rendered before its persistent queue guidance, plus blocking background-output waits with elapsed/remaining-work scrollback markers that refresh uncommitted subagent counts in place and never move below queued user input, instant three-source `/tasks`, asynchronous tool-free display-only `/recap` with stale-result suppression, busy-safe `/btw <question>` side questions with isolated history, non-executing tool definitions, read-only result viewers, and success/failure JSONL artifacts, plus turn-end isolated ghost prompt suggestions with stale suppression, prefix shrinking, divergence hiding, Tab/Right acceptance, and Esc dismissal; pristine pre-activity Ctrl-C restores text/images, removes optimistic transcript and persisted prompt state, ignores orphan events, skips queued/minimal-committed turns, and follows the default-on remote `cancel_rewind_enabled` plus ACP `cancelRewind` capability; TUI startup shows one sanitized source-ordered tip using a persistent cross-session cursor, then falls back to up to three current-version changelog bullets when no tip is available, while remote announcements take display priority and `/release-notes` retains the Markdown view; TUI `/rewind` and empty-composer double-Esc open a newest-first picker with active-turn cancellation, file-aware all/conversation/files modes, conflict preview, append-only branch restoration, and prompt refill, while `/jump` provides oldest-first turn previews whose Enter action and user-message double-click load a draft-preserving previous-prompt editor that conversation-rewinds and resubmits changed Unicode/multiline text; the persisted `/timeline` sidebar adds width-gated, windowed per-turn ticks with viewport-aware highlighting, chevron/tick navigation, trailing-turn top alignment, resize-stable anchors, and hover previews; `/settings` plus its reference aliases opens a compact keyboard panel over twenty persisted UI/tool preferences, including live default permission preselection, a catalog-backed default-model picker with active-session switching and no-override clearing, a live fork-secondary-model override, capability-gated live Plan mode and voice-language preferences, and session-scoped multiline input with atomic rollback, immediate inverted scrolling, bounded scroll speed/line steppers, scroll-input mode and selection-mode changes, full-screen transcript refolding for grouped tool verbs and collapsed edit blocks, and live `pager.toml` manual-fold pinning with viewport anchoring as streaming groups grow, plus live default-on contextual undo and send-now hints with atomic persistence, restart-scoped startup tips, remembered tool approvals, ask-question timeouts, and hunk tracking, with atomic voice-language updates applying to the next capture; `/docs` plus its aliases provides the reference 24-entry built-in guide catalog, list/detail navigation, exact title routing, web aliases, and browser fallback; `/config-agents` (`/agents`) and `/personas` provide fuzzy live agent inspection plus merged bundled/project/user persona browsing, validated creation, safe source viewing, and guarded deletion; `/model`/`/m` and `/effort` provide filtered model-specific pickers and direct arguments, persist default-only selections, migrate completed provider history to a fresh response chain, and update future subagents; `/help`, `/session-info`, and `/context` views, durable `/rename`/`/title`, rule-preserving `/always-approve` and `/auto` toggles with visible badges and atomic `[ui].permission_mode` persistence/rollback, immediate `/vim-mode` navigation toggling with atomic `[ui].vim_mode` persistence/rollback, persisted `/compact-mode` with short-terminal auto-compact and rollback, default-on persisted `/timestamps` display toggling with rollback, live persisted `/theme`/`/t` palette selection plus automatic dark/light mapping controls with cycling, aliases, conditional live application, and rollback, gated `/toggle-mouse-reporting` terminal-capture toggling from any focus, idempotent `/plan [description]`, shared read-only Plan and persisted `/transcript`/`/log` viewers, and local `/quit`/`/exit`, question-aware input arbitration plus Bubble Tea v2 full-screen streaming, control-safe theme-aware Markdown styling with ANSI-aware wrapping, safe direct-terminal and tmux 3.4+ OSC 8 Markdown/bare-URL/complete quoted-file hyperlinks, Unicode input with width-safe cursor navigation, middle insertion, Home/End, Delete/Backspace, line clearing and bounded Ctrl/Cmd-Z undo across prompt and structured-response modes, responsive multiline prompt rendering with Shift/Alt-Enter and trailing-backslash newlines plus Ctrl-M or `/multiline`/`/ml` Enter/send inversion, durable workspace-scoped Up/Down prompt history with trim-keyed deduplication, `/history` fuzzy prompt search and Vim-mode `/` incremental regex scrollback search with match navigation, Tab-to-scrollback and Tab/Space-to-prompt focus navigation with mode-independent line/half-page/page movement plus opt-in Vim j/k/g/G navigation, content-pane mouse-wheel scrolling with timing-based auto classification, forced wheel/trackpad pricing, configurable reference-scaled speed, line count/direction and streaming viewport anchoring, visible-pane linear mouse drag selection with width-safe highlighting and OSC 52 copy, configurable flash/hold behavior, scrollback-scoped opt-in Ctrl-R terminal mouse-capture release, URL/word double-click, rendered-line triple-click, and rendered-table cell/rectangle/whole-table TSV selection, click actions for approval prompts, structured question options and plan-review outcomes, cancellation, status and approval UI, in-place structured question selection, persisted Shift-Tab Plan mode, visible mode/focus badges, dedicated full-plan approve/revise/abandon review, cancellable raw/enhanced memory-note review, default-on ordered read/search/list/web/memory/subagent verb groups across live and restored transcripts with group-level minimal-mode expansion and exact-URL distinct successful WebSearch citation counts with call-count fallback, and default-off compact `Edit file +N/-M` rows for exact-text/hashline edits across live and restored transcripts with full minimal-mode expansion and consecutive normalized same-file coalescing work; focused fullscreen scrollback also toggles the visible folded tool, verb, or edit group in place while preserving viewport position; inline media remains |
| MCP | partial | Stdio, Streamable HTTP and standalone SSE lifecycle, endpoint/origin validation, session/protocol headers, JSON/SSE responses, version negotiation, paginated live tool/resource/prompt discovery, resource reads and subscriptions, prompt rendering, multimodal tool results, tool list-change reload with refreshed ACP entries, ACP-advertised in-process SDK servers from `_meta["x.ai/mcp/servers"]` over reverse `x.ai/mcp/sdk_call`, shared discovery/adapters/disable policy and hot-reload preservation, real-time `x.ai/mcp/init_progress`, full `x.ai/mcp/servers_updated` catalog pushes, post-session `x.ai/mcp_initialized` signal, shutdown, permission-gated sampling, top-level `gork mcp list/add/remove/doctor` with user/project scope, legacy `--command`/`--args` and `--url`/`--type` add forms, and redacted connectivity diagnostics, session-scoped ACP list/direct-call/resource-read, local-only auth status/unsupported trigger, persistent local server/tool toggle with catalog-refetch notification and server-status transitions plus local server upsert/delete with atomic config writes and live reload, rollback-safe client config replacement, plugin-driven base config hot reload preserving session overrides, internal global/project-scoped disk reload across matching live sessions, and content-based automatic local config reload for CLI and ACP sessions, project TOML/Claude/Cursor/hierarchical `.mcp.json` discovery, compatible precedence/env expansion, enabled plugin file/inline MCP configs, and name-level fail-closed folder-trust filtering work; the SDK reverse transport now also carries server notifications through `x.ai/mcp/sdk_message` with permission-gated sampling and protocol-level unsupported-method responses for other server-initiated requests, plus store-backed HTTP/SSE Bearer attach from `$GROK_HOME/mcp_credentials.json`, RFC 8414/9728 discovery with DCR+PKCE loopback enrollment, ACP `auth_status`/`auth_trigger` and TUI `I` wiring, and one-shot 401 refresh (with metadata rediscovery), and `gork mcp doctor` `oauth credentials` auth-state checks (static/env/store; enroll hint when missing), while agent-level ACP pool, managed connectors, and captured server-specific edge cases remain |
| ACP | partial | Protocol v1 JSON-RPC stdio plus authenticated `/ws` WebSocket serving with bearer/query tokens, text or UTF-8 binary messages, keepalives, single-client replacement, and persistent sessions across reconnects supports local `/privacy` and `/doctor` (`/terminal-setup`) discovery and completion without inference, plus independent sessions, client-supplied UUID/configured-model startup metadata, new/load catalog-keyed active and available model state with context/reasoning metadata, allowed/hidden/disabled glob filters, per-model hiding, hidden-but-explicit selection, visible selectable fallback when the configured default is filtered out, live local/managed/requirements model-catalog reload with `x.ai/models/update` plus internal config and external-cache reload endpoints, idle fallback/default switching, busy-turn deferred switching and future-subagent propagation, same-family persisted-model fallback with fresh response chains and live `model_auto_switched` notifications, and prompt blocking for incompatible persisted models or all-excluded allowlists until manual selection, plus idle-only persistent `session/set_model` switching with completed text/image history transfer, canonical `_meta.reasoningEffort` overrides, future subagent/goal-role default updates, and broadcast-only `model_changed` notifications, persistent paginated/faceted list metadata, summary views and live/dormant roster, load with completed text/image-history plus subagent/task lifecycle replay or relay-requested `noReplay`, resume without replay, prompt continuation, busy-rejected resident and atomic offline `x.ai/session/repair` with dry-run/tool-pair reports and safe response-chain reset, persistent default/ask/plan modes with same-turn instruction changes, `x.ai/exit_plan_mode` reverse approval, blocking `x.ai/ask_user_question` with pending/resolved interaction notifications and all four response paths, and capability-gated fail-closed `x.ai/folder_trust/request` decisions with workspace deduplication and post-grant component reload, new/load/resume `_meta.yoloMode` and `_meta.autoMode`/`auto_mode` overrides plus connection-scoped fire-and-forget `x.ai/yolo_mode_changed` ask/always-approve/auto updates with explicit-auto and yolo precedence and managed runtime gates, busy-safe history-isolated `x.ai/btw` side questions with close-time cancellation, FIFO text/image `x.ai/interject` steering with multi-client broadcasts and late-turn fallback, fire-and-forget `x.ai/recap` with display-only success/unavailable session updates, automatic idle/new-turn gates, stale-result suppression, and close-time cancellation, bounded tool-free `x.ai/suggestPrompt` prediction with generation echo, isolated history, deterministic filtering and close-time cancellation, shell-aware `x.ai/suggest` history/PATH/file completion with atomic token replacements, truncation signals, deterministic token-only mode, and optional two-second history-gated AI completion, `/compact`, `/always-approve` with `/yolo` alias, `/context`, `/session-info` with `/status` and `/info` aliases, enabled `/flush` and `/dream`, `/memory` browse plus session-scoped on/off toggling, capability-gated `/plugins` lifecycle commands with `/plugin` and `/reload-plugins` aliases, and `/loop` command discovery plus `x.ai/memory/flush`, bounded history-isolated `x.ai/memory/rewrite`, and reference-shaped memory file/dream updates, text/base64/remote-image prompt blocks, embedded text context, streamed text/tool lifecycle updates with native image/PDF page content, cancellation, standard and idempotent extension close, session info/rename/delete/search with live input-token usage, prompt history, bounded streaming `x.ai/search/content`, stateful routed `x.ai/search/fuzzy/*` file picking, session-bound LSP-backed `x.ai/code/*` definition/reference/symbol navigation and status, opt-in deduplicated `x.ai/git_head_changed` startup/tool/external-HEAD notifications, auth logout/external-clear runtime refresh with orphaned team-policy cleanup and static API-key preservation, refresh-aware `x.ai/billing` credit usage and `x.ai/auto-topup-rule` proxying with live subscription/on-demand metadata, MCP server replacement plus persistent local server/tool toggle and server upsert/delete, persistent skill add/remove/reset/toggle, plugin list/local-path/enable/disable/reload actions plus durable installed-update notifications, hook list/reload/enable/disable/source-toggle/trust/custom-path actions, background-terminal task snapshots/list/kill plus persisted backgrounded/completed notifications, typed subagent get/list-running/cancel snapshots with live turn/token/context/tool/error metrics, reference-shaped persisted spawned/finished plus live progress notifications, serialized completion auto-wakes, real-time rate-limited monitor drains, and deduplicated scheduled-prompt injection with scheduler deletion, confined list/exists/read/write/delete filesystem extensions, authenticated `x.ai/workspaces/list` against grok.com `/rest/workspaces` with `no_oauth`/`error` partial degradation, client-provided stdio/HTTP/SSE MCP servers and tool-correlated permission requests; opt-in cloud conversation listing via `GROK_SESSION_LIST_CONVERSATIONS`/`GROK_CHAT_MODE`/`--chat` merges chat rows into `x.ai/session/list` with starred/workspace facets, `starred`/`workspace` filters (single workspace pushdown), partial `no_oauth`/`timeout`/`error` reporting, `kind: "chat"` rename/star/soft-delete via `x.ai/session/rename` (`starred` bool) /`delete`, thin `session/load`/`resume`/`session/new` for chat kind with `/rest/modes` picker overlay, and process-wide `--chat` forcing chat list/new plus local-Build refusal (cloud transcript replay remains) |
| Web search | done | `web_search` calls the Responses API native search tool with optional domain filters and reference-compatible prompt output, supports `[models].web_search` selection plus per-request environment credential refresh, and falls back to the active Responses model |
| Web fetch | partial | Permission-gated public HTTP(S) fetches enforce 2,000-character URLs, the reference default-off env/TOML/remote gate, startup and per-ACP-session remote refresh, remote proxy/domain fallbacks, documentation-domain policy with host/path overrides, configurable HTTP(S) proxying, single-label rejection, HTTPS upgrades, same-host bounded redirects, DNS/dial SSRF checks, 10-MiB bodies, structured HTML-to-Markdown conversion, relative links, executable/embed stripping, binary rejection, context-bounded previews, recoverable overflow artifacts, validated PDF/image/video session downloads and a 15-minute/128-page successful-text cache work |
| LSP tools | done | Framed stdio/TCP JSON-RPC, configurable per-server workspace/startup/shutdown and bounded crash restart with document replay, initialization options, dynamic settings and section queries, document sync, hover, definitions, references, symbols, published diagnostics, confined `workspace/applyEdit`, user/current-project/plugin config discovery with compatible precedence, folder-trust gating, and transactionally replaced plugin client sets are covered by tests |
| Planning / goals | partial | `todo_write`, active-goal state, `update_goal`, bounded autonomous `--goal` continuation, optional persisted parent-and-role token budgets with live ACP usage updates, persisted UUID v4 identity plus worker/verify/finished-role metrics, active-role snapshots, latest-verdict/details fields, safely replayed latest verifier gaps, and a persisted bounded first-response breadth anchor for delta-aware re-verification, pending-todo-aware premature-stop detection and stronger continuation, safe 8-KiB-capped next-step mining from planner checklists, private per-Goal implementer scratch with literal `{SCRATCH}` plan paths, read-only verifier access, restore recreation, squat rejection, and terminal cleanup, persisted soft/hard re-verification escalation after refuted worker rounds, session-local three-attempt block confirmation with blocked-over-completed precedence, explicit persisted user/back-off/no-progress/infra pause variants with legacy migration and safe in-flight restart degradation, and harness-owned completion verification with configurable 1–5 read-only skeptics, majority refutation, bounded actionable retry gaps, strict verdict and per-skeptic failure refutation, whole-harness fail-open, configurable attempt caps, repeated-gap no-progress pauses, Git-baseline cumulative patches, bounded non-Git modification-time evidence, complete tracked/untracked path prompts, plan-baseline diffs, ordered private details artifacts, validated atomic goal-state resume, persisted skeptic-0 delta rechecks with cold fallback, stable round-robin role model/harness pools, a current-model kill switch, fail-closed automatic planning with immutable private baselines and resume retries, fail-open one-shot verified-achievement summaries, fail-open cadence-triggered structural strategist notes with bounded cap/stall bonuses, persisted role/classifier/skeptic telemetry, and live ACP `goal_updated` snapshots work; durable tool-driven plan mode with `.grok/plan.md`-only mutation gating, same-turn enter/exit behavior, dedicated TUI review, structured `ask_user_question` interviews with config/env/requirements timeout policy across ACP, TUI, REPL, one-shot and goal-mode terminals, `scheduler_create`/`list`/`delete`, durable scheduler state, `/loop` expansion, and serialized scheduled continuation across one-shot headless, REPL, TUI, and ACP sessions also work |
| Skills / AGENTS.md | partial | Compatible home-level and Git-root-to-CWD scoped instruction/rules and skill/flat-command discovery, deeper-scope precedence, same-scope copied-directory rekeying with display labels, `GROK_HOME`, vendor-default denylisting, absolute-path-labelled injection, Git-native instruction ignore filtering, `[skills]` custom paths/ignore/disabled config, normalized names/descriptions/scalar-or-list paths, optional frontmatter metadata, `when-to-use` hints, user/model invocation gates, bare or scope-qualified tool/slash invocation with arguments, Skill-directory/session/plugin-root/plugin-data substitutions, reference skill envelopes, lazy loading, `paths`-gated activation, direct/deep dynamic discovery, per-vendor TOML/environment/remote gates with per-ACP-session refresh, background add/change/delete watching, enabled local plugin skill/command discovery with manifest containment, capability-gated ACP command discovery with `input.hint`, source metadata and collision-safe skill names, ACP list/config plus atomic add/remove/reset/toggle with live-session rebuilds, enabled/disabled plugin inventory with trust/component summaries, persistent path/enable/disable/reload actions with immediate skill plus hot MCP/LSP/hook/agent refresh, trust-gated CLI/ACP direct local/Git and bare/qualified marketplace install with pinned/branch update, reference-shaped installed/available CLI JSON listing, confirmed repo uninstall with `rm`/`remove` aliases, manifest-version Git tag creation with dirty-tree, force, dry-run, and origin-push handling, data cleanup, atomic registry and safe local snapshot refresh, CLI enable/disable, installed component details, strict root/fallback manifest validation, ACP plus CLI local/Git marketplace lifecycle with ordered Grok/Claude settings imports, sticky env/remote-gated official registration, and sanitized SHA-gated v1 component catalogs, command/HTTPS hooks with explicit-deny/fail-open execution, durable disablement, compat-gated global/trusted-project/custom/plugin discovery and lifecycle/failure/permission-prompt/permission-denied/task-complete/compaction/subagent/agent-error/idle events, session-registered concurrent `x.ai/hooks/run` gates and observe-only `x.ai/hooks/event` callbacks with bounded fail-open replies and child inheritance, and callable built-in/project/user/bundled/plugin agent catalogs work |
| Git / worktrees / hunk tracking | partial | Git tracked/staged/untracked hunk parsing, stable IDs, file summaries, mixed-source per-hunk agent attribution that survives staging and session reload, HEAD-aware stale attribution cleanup with persistent agent-file identity, zero-based prompt-index turn actions, per-turn pending and accepted/rejected session summaries, bounded baseline/current content views with explicit missing/binary/too-large/LFS/symlink states, bulk dirty-file content queries, reference-shaped ACP hunk and worktree-management DTOs, safe hunk/file/turn/all actions, and client-advertised `agent_only`/`all_dirty`/`off` tracker lifecycles work; ACP Git root/status/current-commit/info/branches/files/diffs/stage/stage-content/unstage/discard/stash/checkout/session-HEAD/commit operations, including colocated Jujutsu status/info/bookmark/commit/restore routing with staging no-ops and explicit unsupported checkout/stash errors, explicit reference-compatible `x.ai/git/serialize_changes` unavailable response, optional Git status patches, default submodule filtering, and aggregate byte/line patch-limit enforcement, plus `gh`-backed PR status and merge-queue lookup work; persisted linked/standalone/git lifecycle, sync/async fork creation, dirty-state copying with macOS/Linux CoW cloning and portable fallback, cancellable background `.gitignore` file copying with skip patterns and status notifications, overwrite/merge conflict reporting, safe removal, GC and registry management, direct `gork worktree list/show/rm/gc/db` administration, durable full-state refs with standalone transfer and linked rehydration, local session resolution/resume, historical HEAD restore with stash protection and idempotent local rehydrate work; remote session archives and filesystem snapshot disposal remain |
| Subagents | partial | Existing Runner/tool infrastructure now powers built-in and custom foreground/background `task` agents with reference-compatible depth-one task removal, tool allow/deny and capability filtering, turn limits, strict runtime-enum validation, profile-aware model/reasoning-effort overrides with provider/context routing, internal harness overrides, inherited context-window/compaction settings, isolated parent-skill snapshots that persist across resume, per-agent skill preloading/discovery/inheritance controls, user/project/local agent-scoped `MEMORY.md` injection with bounded isolated file access, parent MCP `all`/`none`/`named`/`except` inheritance filters with plugin-agent isolation, private user/trusted-project agent-owned named and inline stdio/HTTP/SSE MCP servers with owned-name precedence and resume-safe cleanup, isolated inline hooks for user/trusted-project agents with plugin rejection and resume preservation, non-plugin `bypassPermissions` with resume persistence, deny-rule/explicit-deny enforcement, and managed-lock downgrading across fresh and persisted resumes, validated per-task cwd rebinding with source-cwd resume preservation, dirty-state-preserving linked-worktree isolation with completion snapshots, safe disposal and cross-process rehydration, durable child JSONL/parent-scoped metadata with model/harness/capability restoration, terminal query and same-type resume after restart, one-shot orphan reconciliation, persisted lifecycle replay, live turn/token/context/tool/error metrics with rate-limited ACP spawned/progress/finished pushes, polling, cancellation, session cleanup, hook events, plugin hot refresh, typed ACP get/list-running/cancel snapshots, and env/TOML/remote-gated ACP/headless/REPL/TUI auto-wake with blocking-consumer, timeout, kill, and queued-result deduplication work |
| Memory / compaction | partial | Responses, Chat Completions and Anthropic usage accounting, compatible 85% threshold resolution, successful-summary fresh-chain compaction, manual `/compact` in REPL/TUI, TUI context status and configurable old-tool-result pruning work; opt-in two-pass compaction starts a bounded asynchronous prefix prefire ten points before the threshold, clones stateful backend history before sampling, validates model/response lineage, merges the new text/tool tail on a fresh chain, and fails open for stale, failed, or multimodal caches with env/local/remote gate precedence; opt-in pre-compaction, configurable idle, and manual `/flush` memory writes use compatible headroom/defaults, isolated no-tool sampling, shared concurrency control, cancellable session timers, `NO_REPLY`/Markdown/size quality gates, exact deduplication, atomic workspace-scoped logs, and bounded first-fresh-turn file-backed injection with env/local/remote/CLI precedence; default-on session-end metadata summaries use rewind-aware logs, real-prompt count/byte gates, synthetic filtering, and idempotent shutdown writes; temporary-directory workspaces remain global-memory capable but skip all workspace/session persistence; `/memory` safely lists source-classified file metadata in REPL/TUI/ACP without a model turn, and `/memory on|off` plus `/mem` aliases atomically restore/remove the store and retrieval tools for the current session without changing disk config; `gork memory clear` confirms and removes workspace, global, or both scopes without following symlinks; always-available `/remember [text]` provides no-argument input mode, bounded history-isolated rewriting with raw fallback, nonce-safe raw/enhanced TUI review, explicit terminal confirmation, cancellation, Markdown normalization, and guarded global `MEMORY.md` append, while the matching ACP rewrite extension enforces the reference 32-KiB input, fixed model/temperature, and 1024-token output; manual, session-end, and configurable idle `/dream` consolidation uses 4-hour/3-log defaults, 32-KB merged inputs, 16-K-character structured outputs, PID/mtime locking with rollback, existing-memory merge, write-before-delete ordering, and a five-minute cleanup guard; enabled sessions register safe `memory_get` with reference optional-range, explicit-zero, full-file trailing-line, and `start`/`all` formatting semantics, plus scan-based text-only `memory_search` with reference age-labelled stale-session warnings, Markdown heading/paragraph/line chunking, overlap, normalized token ranking, structural filtering, configurable half-life decay, per-source weights, opt-in MMR diversity, deterministic bounds, and local/remote search defaults; background startup GC preserves the current and session-bearing workspaces while removing eligible `tmp*` and old empty orphan directories; durable workspace `index.sqlite` FTS5 reindex/BM25 candidate narrowing for `memory_search` landed (`[memory.embedding]`; unset model keeps text-only; ephemeral/index errors fail open to scan ranking); portable `chunks_embedding` BLOB KNN, hybrid score merge helpers, flush `semantic_dedup_threshold` (default 0.92), and live semantic flush dedup when a MemoryEmbedder is set landed; OpenAI-compatible `APIEmbeddingProvider` + hybrid FTS/KNN `Store.Search` merge land (fail open without embedder); session wiring and sqlite-vec accel remain; `memory_edit` covers approved, atomic line-range replacement and deletion across the allowed memory files |
| OS sandbox | partial | Model-started foreground, background, monitor, legacy shell, and subagent shell processes support fail-closed `workspace` and `read-only` profiles through macOS Seatbelt or Linux bubblewrap; macOS and Linux also support `strict`, limiting reads to the workspace, Gork state, temporary directories, and required system runtime paths while read-only and strict isolate child networking; Linux bubblewrap children install per-child seccomp namespace lockdown (unshare/setns/clone3/CLONE_NEW*) and, for read-only/strict, stacked child network deny (connect/bind/sendto/sendmsg/listen/accept/accept4) alongside `--unshare-net`; parent-process Landlock FS allowlists apply best-effort on Linux for workspace/read-only/strict (warn+continue when unsupported; network unrestricted); Linux also fail-closed parent bwrap re-exec for hook write-deny (`$GROK_HOME/hooks`, `hooks-paths`, and listed sources mounted read-only via `__GROK_INSIDE_BWRAP`); custom `$GROK_HOME/sandbox.toml` / `.grok/sandbox.toml` profiles that `extends` a built-in contribute extra Landlock `read_only`/`read_write` paths and optional `restrict_network` (Linux fail-closed when Landlock cannot apply; child shells wrap with the extends base); custom TOML `deny` paths are Linux parent-bwrap bind-over blocked with mode-000 placeholders (exact paths plus launch-time glob expansion) |
| Workspace server | planned | Hub server, previews, remote workspaces and supervision remain |
| Markdown/media/Mermaid | partial | TUI headings, emphasis, links, ordered/unordered lists, quotes, pipe tables, inline code and fenced code render with streaming-safe markers and visible-width wrapping; closed flowchart, graph, state, sequence, class, ER, C4 Context/Container/Component/Dynamic/Deployment, requirementDiagram, info, pie, timeline, journey, Gantt, packet-beta, block-beta, kanban, gitGraph, sankey-beta, quadrantChart, radar-beta, xychart-beta, and mindmap Mermaid fences render as bounded Unicode relationship, architecture, requirement, version, percentage, chronology, task, bit-range, commit-history, flow, grid, board, coordinate, axis/value, series, or hierarchy art with safe source fallback; image attachments render inline via kitty Unicode placeholders on kitty terminals and via iTerm2 or sixel protocol blocks in native scrollback mode, with metadata fallback elsewhere; tool-result images persist to session assets for inline replay, while non-Kitty fullscreen pixel overlays remain |
| Telemetry/privacy hard-offs | done | No analytics, research uploads, feedback/review uploads, auto-update or retention opt-in code exists; `/privacy` reports and confirms the enforced opt-out state locally, explicit `/feedback` text and review citation events are stored only in the local session log, while review comment bodies are discarded |

Pager scroll compatibility includes default-on `[scrollback.scroll].anchor_on_fold`
with reference-style minimal visibility scrolling when explicitly disabled, plus
the default centered `follow_indicator`, its `none` opt-out, and configurable
bottom overscroll follow restoration. Default-on `[ui].page_flip_on_send` pins a
new prompt to the viewport top until the streamed reply fills the rows below it
and bottom follow resumes; disabling it keeps bottom follow or an active manual
reading position unchanged. Default-off `[ui].combine_queued_prompts` combines
consecutive plain queued follow-ups into one model turn while preserving their
individual user bubbles and stops before a later image prompt.

Desktop notifications resolve `[ui.notifications] method` over reference terminal
detection: iTerm2/WezTerm/Warp use OSC 9, Kitty OSC 99, Ghostty/VTE/Terminator/foot
OSC 777, Grok Desktop stays silent, every other brand rings the bell, and Zellij
forces the bell because it cannot forward OSC notifications. Sequences are wrapped
in tmux DCS passthrough with doubled escapes when tmux backs the session. Emission
honours `condition` (`unfocused` with an `idle_threshold_secs` clock, `always`, or
`never`) and the `events` list across `turn_complete`, `agent_error`,
`approval_required` — one notification per queued permission batch — and
`session_ready`; focus reporting is only requested when the condition depends on
it. An unknown enum value discards the whole table and keeps inherited defaults,
matching the reference decoder. Default-on `progress_bar` drives the OSC 9;4 tab
indicator on Ghostty, WezTerm, and iTerm2 3.6 or later — older iTerm2 renders the
parameters as alert text, so it is treated as incapable — re-sending the
indeterminate sequence on the reference five-second keep-alive while a turn runs,
clearing it when the turn ends or the session exits, and using tmux passthrough
only for servers new enough to forward it. Default-on `sleep_prevention` holds an
idle-sleep inhibitor for the same window through `caffeinate` on macOS and
`systemd-inhibit` on Linux; the macOS assertion also waits on the Gork process so
a crash cannot leave the machine awake. Inhibit and release are idempotent, an
unavailable platform command is probed once and then left alone, and other
platforms are no-ops. `[[ui.notifications.hooks]]` entries run `sh -c` in a
detached process group with `GROK_EVENT`, `GROK_MESSAGE`, and `GROK_SESSION_ID`
exported, null standard streams, and a whole-tree kill once `timeout_secs`
(minimum one second, default ten) elapses; spawn failures, non-zero exits, and
timeouts stay out of the session. Each hook filters on its own `events` list —
empty matches every event — and default-on `only_unfocused`, independent of
`method` and `condition`, since the reference dispatcher lives outside its
notifications module and only the field semantics are documented. Default-on
`[ui.notifications.title]` composes the terminal title from its `items` order —
`action-required`, `spinner`, `activity`, `session-name`, `cwd`, `model`,
`turn-timer`, and `grok` — joined with ` - `, falling back to the product name
when nothing contributes, truncating session names at 40 and model, cwd, and
tool names at 30 runes, stripping control characters so a remote-sourced title
cannot escape the OSC sequence, emitting only on change, and resetting to `gork`
at exit. The spinner and the half-second `⚠ Action Required` blink animate on a
250-millisecond tick that is only armed while a turn runs or a prompt waits
unfocused; a focused terminal shows the label statically. Activity reports
`Thinking`, `Waiting`, per-tool `Running: <tool>` (or a description subject with
an ellipsis when tool input provides `description`), and `Responding` with
parked-wait then thinking then tool then responding priority. `task_complete` fires when a
background subagent finishes, successfully or not, while foreground children stay
silent because the turn itself already reports. With `[ui.notifications] session_recap`
(and the feature gate), an automatic return-from-away recap pre-generates while
unfocused past `session_recap_threshold_secs` (default 30s) and also runs on
focus-gained when due: once per away period, 90s retry backoff after attempts the
shell may no-op, UI-idle (no modal/question/live background work), and shell gates
of ≥3 completed turns, ≥3 minutes since the last main turn, and no repeat until a
newer turn completes. Manual `/recap` still works whenever the feature is on.

Compatibility is verified with Go unit/integration tests and will additionally
use captured protocol fixtures from the Rust implementation. A status is only
changed to **done** after its relevant compatibility tests exist.
