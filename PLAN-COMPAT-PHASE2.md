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
| 1 | **MCP OAuth enrollment** | MCP | Unlocks authenticated remote MCP servers; user-visible | L (multi-PR) |
| 2 | **Linux cgroups for shell** | Shell execution | Completes shell isolation story after wrap | L |
| 3 | **Landlock + seccomp** | OS sandbox | Hardens existing bubblewrap/Seatbelt profiles | XL |
| 4 | **Cloud ACP / conversations** | ACP | Large protocol surface; defer until local ACP stays green | XL |
| 5 | **Remote relay / workspace hub** | Session / workspace | Depends on product decisions | XL |
| 6 | **Vector memory** | Memory | Research + storage + retrieval | XL |
| 7 | **Fullscreen media / inline video** | Markdown/media | Terminal capability + UX heavy | XL |

Deferred unless explicitly requested: Cursor CLI session discovery, WezTerm kitty XTVERSION probes, Wayland data-control, sandbox.toml profile-conflict doctor.

---

## Phase 1 — MCP OAuth (active plan)

### Goal

Bring Go MCP auth from “local-only / unsupported trigger” to reference-shaped
OAuth enrollment for HTTP/SSE servers, with durable tokens and ACP/TUI status.

### Slice plan

1. **Discovery / inventory** — done (see Phase 1 notes below).

2. **Token store + config** — done (store + atomic 0600 writes; TOML oauth_*
   fields parsed; inspect/doctor redaction cells still open).

3. **Enrollment flow** — done: discovery + DCR + PKCE loopback
   (`AuthenticateMCPServer`). Pasted-callback / cross-process dedup still open.

4. **Runtime use** — done for attach + 401 refresh (TokenURL or rediscovery).
   Doctor auth-state cell done (`gork mcp doctor` `oauth credentials` check).

5. **ACP / TUI** — done for `auth_status` / `auth_trigger` / TUI `I`.

### Done when

- [x] Authenticated HTTP MCP server can be added, enrolled, and called in tests.
- [x] Token refresh covered by unit/integration tests.
- [x] COMPAT MCP row mentions OAuth enrollment (no longer “remain”).
- [x] README documents MCP auth UX briefly.
- [x] `gork mcp doctor` reports an `oauth credentials` check for HTTP/SSE
      (static bearer / env / store; fail-closed with enroll hint).

### Phase 1 notes

- Rust store: `$GROK_HOME/mcp_credentials.json`, key `{name}:{url}`, flattened
  map of `StoredCredentials` (`client_id`, `token_response`, `granted_scopes`,
  `token_received_at`). Loopback+PKCE+DCR enrollment; no device-code for MCP.
- Go now mirrors the on-disk shape and attaches/refreshes tokens in
  `internal/mcp` (`credentials.go`, `http_auth.go`). Pasted-callback /
  cross-process enroll dedup and inspect oauth_* presence cells remain.

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
- [ ] Full cloud transcript replay (follow-up).

### Out of scope for slices 1–4

Fetching and replaying grok.com message history into the local runner.

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

---

## How to run this plan

```text
# Start Phase 1
Tell the agent: 「按 PLAN-COMPAT-PHASE2.md 做 Phase 1 slice 1」

# Or jump
「先做 Linux cgroups」
```

Update this file’s checkboxes as slices merge.
