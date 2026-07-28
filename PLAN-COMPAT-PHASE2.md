# COMPAT Phase 2 — thematic backlog

One-round doctor/wrap surface closed at `5c6557d`. Remaining gaps need
**one topic per branch**, multi-PR slices, and explicit done criteria.

## Working rules

- No 3-minute “ship anything” loop.
- One theme at a time; open `compat/<theme>` from `main`.
- Each PR: implement + tests + `go test ./...` + COMPAT/README update.
- Prefer Linux-first when OS-specific; document Windows/macOS as follow-ups.
- Align to Rust under `/tmp/gork-build/crates/codegen` when present; otherwise pin behavior in the PR description.

## Priority queue

| # | Theme | COMPAT area | Why now | Rough size |
|---|--------|-------------|---------|------------|
| 1 | **MCP OAuth enrollment** | MCP | Unlocks authenticated remote MCP servers; user-visible | done |
| 2 | **Linux cgroups for shell** | Shell execution | Completes shell isolation story after wrap | L |
| 3 | **Landlock + seccomp** | OS sandbox | Hardens existing bubblewrap/Seatbelt profiles | done |
| 4 | **Cloud ACP / conversations** | ACP | Large protocol surface; defer until local ACP stays green | XL |
| 5 | **Remote relay / workspace hub** | Session / workspace | Depends on product decisions | XL |
| 6 | **Vector memory** | Memory | Research + storage + retrieval | done (sqlite-vec optional) |
| 7 | **Fullscreen media / inline video** | Markdown/media | Terminal capability + UX heavy | done |

Shipped: SSH WezTerm XTVERSION doctor recovery (`compat/wezterm-ssh-xtversion`);
Cursor CLI session discovery (`compat/cursor-cli-session-discovery`).

Shipped deferred doctor probes: sandbox.toml profile-conflict, Wayland data-control
(`compat/wayland-data-control-doctor`), local WezTerm Kitty keyboard warning
(`compat/wezterm-kitty-keyboard-doctor`), SSH WezTerm XTVERSION recovery
(`compat/wezterm-ssh-xtversion`), doctor Facts/Human `xtversion` (`compat/doctor-xtversion-fact`). ffmpeg PATH probe (`compat/ffmpeg-doctor`).

---

## Phase 5 — Remote relay / workspace hub (blocked)

Product-coupled (`computer-hub.grok.com`, OIDC hub auth, preview supervisor).
Go keeps fail-closed headless + `workspace_exposure: false`. Do not dial hub
without an explicit product unlock.

### Fail-closed surface (shipped on `compat/workspace-hub-fail-closed`)

- [x] `internal/workspacehub` status + `Dial` refuse (`ErrProductBlocked`)
- [x] `gork inspect` `workspaceHub` cell + ACP `x.ai/workspace_hub/status`
- [x] Leader `workspace_exposure: false` documented
- [ ] Real hub WebSocket client / preview supervisor (needs product unlock)

---

## Phase 1 — MCP OAuth (active plan)

### Goal

Bring Go MCP auth from “local-only / unsupported trigger” to reference-shaped
OAuth enrollment for HTTP/SSE servers, with durable tokens and ACP/TUI status.

### Slice plan

1. **Discovery / inventory** — done (see Phase 1 notes below).

2. **Token store + config** — done (store + atomic 0600 writes; TOML oauth_*
   fields parsed; `gork inspect` MCP auth presence cells done — no secrets).

3. **Enrollment flow** — done: discovery + DCR + PKCE loopback
   (`AuthenticateMCPServer`) plus in-process single-flight and Unix
   `mcp_auth_*.lock` cross-process dedup with credential-store poll while
   waiting on the loopback callback, plus pasted-callback UX
   (`SubmitMCPAuthCallback`, `PastedInput`, ACP `x.ai/mcp/auth_submit`).

4. **Runtime use** — done for attach + 401 refresh (TokenURL or rediscovery).
   Doctor auth-state cell done (`gork mcp doctor` `oauth credentials` check).

5. **ACP / TUI** — done for `auth_status` / `auth_trigger` / `auth_submit` /
   TUI `I`.

### Done when

- [x] Authenticated HTTP MCP server can be added, enrolled, and called in tests.
- [x] Token refresh covered by unit/integration tests.
- [x] COMPAT MCP row mentions OAuth enrollment (no longer “remain”).
- [x] README documents MCP auth UX briefly.
- [x] `gork mcp doctor` reports an `oauth credentials` check for HTTP/SSE
      (static bearer / env / store; fail-closed with enroll hint).
- [x] Concurrent enrollments share one browser flow (in-process + Unix flock
      + store poll) on `compat/mcp-oauth-enroll-dedup`.
- [x] Headless clients can paste a callback URL via `SubmitMCPAuthCallback` /
      ACP `x.ai/mcp/auth_submit` on `compat/mcp-oauth-pasted-callback`.
- [x] `gork inspect` MCP cells expose auth=static|env|stored|oauth_byo|none
      plus env-var names only (never tokens/headers/secrets) on
      `compat/mcp-oauth-inspect-presence`.

### Phase 1 notes

- Rust store: `$GROK_HOME/mcp_credentials.json`, key `{name}:{url}`, flattened
  map of `StoredCredentials` (`client_id`, `token_response`, `granted_scopes`,
  `token_received_at`). Loopback+PKCE+DCR enrollment; no device-code for MCP.
- Go now mirrors the on-disk shape and attaches/refreshes tokens in
  `internal/mcp` (`credentials.go`, `http_auth.go`), with enroll dedup,
  pasted-callback completion for unreachable loopbacks, and `gork inspect`
  auth/oauth_* presence cells (no secrets).

### Out of scope for Phase 1

Managed connectors, agent-level ACP MCP pools, plugin marketplace OAuth.

---

## Phase 2 — Linux cgroups (active)

### Goal

Apply cgroup v2 limits to model-started shell / background tasks on Linux
(memory.high/max as reference; cpu controller enabled but no cpu.max), with
clear no-op on unsupported hosts.

### Done when

- [x] Detect cgroup v2; skip/warn otherwise.
- [x] Foreground + background shell paths covered by tests (or integration with fake fs).
- [x] COMPAT Shell row drops “cgroups remain” (shell + ACP terminals + OOM-137;
  cpu.max still deferred to match reference).

### Notes

- Shared ProcessManager cgroup for FG/BG/monitor; legacy `shell` tool uses a
  per-invocation guard. ACP PTY/piped terminals share a best-effort
  `tools.NewShellCgroup` guard. inotify `memory.events` OOM signaling kills the
  newest FG/BG shell child or ACP terminal as exit 137 / signal `oom`
  (`compat/acp-terminal-oom`). cpu.max still deferred.

---

## Phase 3 — Landlock + seccomp (done)

### Done

- [x] Parent-process Landlock FS allowlists on Linux for built-in profiles
      (`ApplyParentLandlock`, BestEffort warn+continue; no parent net restrict)
      on `compat/linux-landlock`.
- [x] Per-child seccomp namespace lockdown inside Linux bwrap
      (`__GROK_SECCOMP_NS__` helper; BPF unit-tested) on `compat/linux-seccomp`.
- [x] Child network seccomp for read-only/strict (`__GROK_SECCOMP_NS_NET__`,
      stacked with namespace lockdown alongside `--unshare-net`) on
      `compat/linux-seccomp-net`.
- [x] Parent bwrap re-exec / hook write-deny for built-in profiles
      (`EnsureParentBwrapHookWriteDeny`, fail-closed; `__GROK_INSIDE_BWRAP=1`)
      on `compat/linux-parent-bwrap`.
- [x] Custom `sandbox.toml` profile Landlock parity (`LoadSandboxTOML` /
      `ResolveSandboxProfile`; parent Landlock from base + extras; Linux
      fail-closed for custom when Landlock cannot apply; child wrap uses
      extends base + `restrict_network`) on `compat/linux-sandbox-toml-landlock`.
- [x] Custom `sandbox.toml` `deny` → Linux parent bwrap bind-over
      (mode-000 placeholders; exact + launch-time glob expand; fail-closed)
      on `compat/linux-sandbox-deny-bwrap`.

### Still open

- [x] sandbox.toml profile-conflict doctor (`SandboxProfileConflicts` →
      `gork doctor` / `/doctor` finding; global wins, project cannot redefine)
      on `compat/sandbox-profile-conflict-doctor`.

---

## Phase 4 — Cloud conversations (active)

### Goal

Merge grok.com cloud chat conversations into ACP `x.ai/session/list`.

### Slice plan

1. **Read-only list + partial status** — done on `compat/cloud-conversations-list`:
   `internal/remote` ConversationsClient, opt-in lane
   (`GROK_SESSION_LIST_CONVERSATIONS` / `GROK_CHAT_MODE`), merge into session
   list, `no_oauth`/`timeout`/`error` partial meta.

2. **Rename / soft-delete** — done: `kind: "chat"` on
   `x.ai/session/rename` / `x.ai/session/delete` maps to PUT + soft DELETE.

2b. **Starred facet + star update** — done on
    `compat/cloud-conversations-starred-facet`: chat rows expose `starred`
    facets, list honors `x.ai/facetFilters.starred`, and rename accepts an
    optional `starred` bool for PUT updates.

2c. **Workspace facet + pushdown** — done on
    `compat/cloud-conversations-workspace-facet`: parse conversation
    `workspaces`, expose `workspace` facets/filters, and push a single
    workspace filter to `workspaceId` on the list API.

3. **Thin chat load/resume + modes catalog** — done: `_meta.x.ai/session.kind=chat`
   on `session/load`/`resume` opens a resident chat session without local JSONL;
   `POST /rest/modes` supplies the model picker when available. Cloud transcript
   replay remains deferred (reference release also hard-offs full chat kind).

4. **Process `--chat` / chat mode** — done: CLI `--chat` sets `GROK_CHAT_MODE`,
   forces session list to chat, opens `session/new` as chat, refuses local Build
   loads, advertises `chatMode` + modes `modelState` on initialize, and rejects
   `--fork-session` / leader conflicts.

### Done when

- [x] Opt-in list merges `kind=chat` rows with httptest coverage.
- [x] Degraded lane reports `_meta["x.ai/partial"]` reasons.
- [x] Rename/soft-delete parity for chat kind.
- [x] Thin chat load/resume + `/rest/modes` picker.
- [x] Process `--chat` / chat-mode ACP defaults.
- [x] Messages API contract + fail-closed ACP hooks on
      `compat/cloud-transcript-messages-contract`
      (`ListMessages`, `x.ai/session/load_history`, session/load partial meta).
- [ ] Full cloud transcript replay into the local runner (needs live API confirm).

### Out of scope for slices 1–4

Injecting grok.com message history into the local runner (contract shipped; replay deferred).

---


## Phase 4b — ACP workspaces list

### Goal

Fetch grok.com workspaces for ACP clients via `x.ai/workspaces/list`, degrading
to empty + `_meta["x.ai/partial"]` when OAuth is unavailable.

### Slice plan

1. **WorkspacesClient + ACP wiring** — done on `compat/acp-workspaces-list`:
   `GET /rest/workspaces` with page/query/kind, success rows, and
   `no_oauth`/`error` partial responses.


## Phase 4c — ACP auth cancel

### Slice

1. **`x.ai/auth/cancel`** — done on `compat/acp-auth-cancel`: idempotent cancel
   of in-flight interactive login with optional `request_seq` scoping.


## Phase 4d — ACP session usage

### Slice

1. **`x.ai/session/usage`** — done on `compat/acp-session-usage`: cumulative
   in-process PromptUsage ledger with partial-cost scrubbing.


## Phase 4e — ACP debug agent

### Slice

1. **`x.ai/debug/agent`** — done on `compat/acp-debug-agent`: process-local
   registry counts for client integration testing.


## Phase 4f — ACP reload workflows

### Slice

1. **`x.ai/internal/reload_workflows`** — done on `compat/acp-reload-workflows`:
   refresh skills and push `available_commands_update` to all live sessions.

## Phase 4g — ACP workflows list

### Slice

1. **`x.ai/workflows/list`** — done on `compat/acp-workflows-list`:
   discovery-only catalog (builtin + project + user `.rhai` meta). Rhai
   execution remains XL.

## Phase 4h — ACP MCP setup

### Slice

1. **`x.ai/mcp/setup`** — done on `compat/acp-mcp-setup`:
   `mcp_preferences.json`, v0 one-select resolve/templates, ACP setup + enable.
2. **`x.ai/mcp/list` setup placeholders** — done on `compat/acp-mcp-setup-list`:
   unresolved schemas appear as `setuprequired` rows with `setup`/`setupValues`.
3. **Plugin-scoped setup collect** — done on `compat/mcp-setup-plugin-collect`:
   executable plugin MCP schemas participate in list/setup with `sourceLabel`.

## Phase 3+ — sandbox / memory / relay

Separate kickoff docs when prior themes land. Do not start in parallel without
an explicit request.

## Phase 6 — Vector memory (done)

### Done

- [x] `[memory.embedding]` + search `vector_weight`/`text_weight` config (Rust defaults;
      unset `model` keeps text-only).
- [x] Durable workspace `index.sqlite` schema (`meta`/`chunks`/`chunks_fts`, no
      `chunks_vec` yet); `OpenIndex` / meta helpers on `compat/vector-memory-schema`.
- [x] FTS5 BM25 + Markdown reindex APIs (`ReindexFile` / `SearchFTS` on
      `IndexDB`); on `compat/vector-memory-fts`.
- [x] Wire FTS into `Store.Search` as candidate narrowing (rankChunks keeps
      decay/weights/MMR; ephemeral and index errors fail open) on
      `compat/vector-memory-fts-search`.
- [x] Hybrid score merge helpers + flush `semantic_dedup_threshold` config /
      fail-open hook (live KNN still needs sqlite-vec + embedder) on
      `compat/vector-memory-hybrid-dedup`.
- [x] Portable `chunks_embedding` BLOB store + brute-force L2 KNN,
      `EmbeddingProvider` / `HashEmbeddingProvider`, `EmbedMissing`, and live
      semantic flush dedup when `Runner.MemoryEmbedder` is set on
      `compat/vector-memory-knn`.
- [x] OpenAI-compatible `APIEmbeddingProvider` + hybrid FTS/KNN merge in
      `Store.Search` when an embedder is configured (fail open otherwise) on
      `compat/vector-memory-embed-api`.
- [x] Session startup wiring: CLI/ACP runners attach `APIEmbeddingProvider`
      from base URL/API key when `[memory.embedding].model` is set on
      `compat/vector-memory-embed-wire`.
- [x] Background `WarmEmbeddings` reindex + EmbedMissing at session startup
      (`compat/memory-embed-missing-startup`).

### Still open

- [ ] Optional sqlite-vec acceleration (deferred; brute-force KNN is fine for
      memory-scale corpora)

---

## Phase 7 — Fullscreen media / inline video (done)

### Done

- [x] Modal overlay escape helpers: centered placement, Kitty transmit/place
      (+ clear), iTerm2 overlay path for non-Kitty pixel overlays on
      `compat/fullscreen-media-overlay`.
- [x] Fullscreen image preview chrome: `/preview-image`, bordered popup, Esc
      close, post-frame pixel flush via `BuildOverlayImageEscapes` on
      `compat/fullscreen-image-overlay-chrome`.
- [x] Click Kitty placeholder rows to open the image overlay on
      `compat/fullscreen-image-click`.
- [x] Video overlay viewer: ffmpeg frame extract, `/play-video <path>`, play/
      pause/seek chrome, tick playback + pixel flush (fails open without ffmpeg)
      on `compat/fullscreen-video-overlay`.
- [x] Prompt-chip hover/focus image preview (`[Image #N]` chips; hover opens
      overlay, click pins until Esc) on `compat/fullscreen-prompt-chip-preview`.
- [x] Session-asset video discovery: `ListVideoAssets` / `LatestVideoAsset`,
      `/play-video` with bare name or empty arg opens newest `videos/*.mp4`
      on `compat/fullscreen-session-video-discovery`.
- [x] Discovery roots at `artifacts/<session>/videos` (web_fetch save path) plus
      `/videos` listing on `compat/fullscreen-session-video-artifacts`.
- [x] Session download discovery: `ListDownloadAssets` / `/downloads` for
      `artifacts/<session>/downloads` on `compat/session-download-discovery`.
- [x] Session image discovery: `ListImageAssets` / `/images`, with
      `/preview-image` falling back to newest artifact image on
      `compat/session-image-discovery`.
- [x] `/preview-image [path|name]` resolves `images/` like `/play-video` on
      `compat/session-image-preview-path`.
- [x] Session web_fetch text discovery: `ListWebFetchAssets` / `/fetched`
      (`/list-fetched`, `/web-fetch-artifacts`) for truncated overflow under
      `artifacts/<session>/web_fetch/` on `compat/session-webfetch-discovery`.
- [x] `/fetched [path|name]` shows capped artifact text (empty lists) on
      `compat/session-webfetch-show`.

### Still open

- (none for Phase 7)

## Phase 5 note

Remote relay / workspace hub remains planned/XL (product-blocked). See queue #5.

## Config alias slices

- [x] `[ui].ui_theme` fills canonical `theme` when `theme` is unset (`theme`
      wins if both set) on `compat/ui-theme-alias`.
- [x] Top-level `[shell_environment_policy]` (inherit/exclude/set/include_only
      + default secret excludes) applied to shell + ProcessManager spawn env on
      `compat/shell-environment-policy`.
- [x] `[ui].simple_mode` (default on; `false` mirrors unset `vim_mode` → on) and
      `[toolset.bash].login_shell_capture` / `GROK_LOGIN_ENV` (default on) on
      `compat/simple-mode-login-shell`.
- [x] Workflow tool `validate_only` + resolve/validate helpers on
      `compat/workflow-validate-only` (full Rhai execution still XL).
- [x] TUI `/workflows` catalog + `/workflow validate <name|path>` on
      `compat/workflow-slash-validate`.
- [x] Workflow execution host + NDJSON Rhai sidecar protocol on
      `compat/workflow-rhai-exec` (`GORK_WORKFLOW_RUNNER` / `gork-workflow-runner`;
      `agent()` → SubagentBackend). The reference `deep-research` script is
      embedded and executable through that path with typed JSON arguments.
      Launches now return immediately into a bounded session-owned manager;
      TUI and ACP `/workflows` list active/recent runs and `/workflow <run-id>
      stop` cancels one. Journal, pause/resume/save, automatic completion
      notices, and the rich run dashboard remain follow-ups. TUI
      `/workflow <name> [args]` launches saved workflows with the reference
      JSON-object, text-fallback, and null argument mapping; in the TUI and ACP,
      unique names that do not collide with builtins or invocable skills also
      resolve directly as `/name <args>`.

## Remaining XL / blocked (do not start without explicit kickoff)

- Full cloud transcript **runner replay**: messages contract + `load_history`
  shipped; inject into local runner only after grok.com `/messages` is confirmed.
- Rhai runner **binary**: Go host is ready; ship/link a real `xai-workflow`
  sidecar build (journal/parallel/UI still deferred).
- Remote relay / workspace hub: fail-closed surface shipped; real dial still
  product-blocked until OIDC hub auth + preview supervisor unlock.

---

## How to run this plan

```text
# Start Phase 1
Tell the agent: 「按 PLAN-COMPAT-PHASE2.md 做 Phase 1 slice 1」

# Or jump
「先做 Linux cgroups」
```

Update this file’s checkboxes as slices merge.
